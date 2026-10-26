package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/middleware"
)

const (
	defaultAuditPageSize = 50
	maxAuditPageSize     = 200
	// Export cap. An audit CSV is opened in a spreadsheet, not processed as a
	// stream, so a file with millions of rows is of no use to anyone. When the cap
	// is reached, the file itself says so (see exportTruncatedNote).
	maxAuditExportRows = 10000
)

// AuditHandler serves the audit_log query. It is the ONLY place in the backend
// that reads that table: until now it was only written to, and querying it
// meant opening psql inside the Postgres container.
type AuditHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
}

type auditEntry struct {
	ID            int64           `json:"id"`
	TS            time.Time       `json:"ts"`
	UserID        *int64          `json:"userId,omitempty"`
	ActorUsername string          `json:"actorUsername,omitempty"`
	IP            string          `json:"ip,omitempty"`
	Action        string          `json:"action"`
	Cluster       string          `json:"cluster,omitempty"`
	Namespace     string          `json:"namespace,omitempty"`
	Resource      string          `json:"resource,omitempty"`
	Result        string          `json:"result"`
	Detail        json.RawMessage `json:"detail,omitempty"`
}

type auditResponse struct {
	Items []auditEntry `json:"items"`
	// NextCursor is the "ts,id" of the last row returned. Empty = no more rows.
	NextCursor string `json:"nextCursor,omitempty"`
}

// auditFilter holds the query-string filters, already validated.
type auditFilter struct {
	actor    string
	action   string
	cluster  string
	result   string
	text     string
	from     time.Time
	to       time.Time
	cursorTS time.Time
	cursorID int64
	limit    int
}

func parseAuditFilter(r *http.Request) (auditFilter, error) {
	q := r.URL.Query()
	f := auditFilter{
		actor:   strings.TrimSpace(q.Get("actor")),
		action:  strings.TrimSpace(q.Get("action")),
		cluster: strings.TrimSpace(q.Get("cluster")),
		result:  strings.TrimSpace(q.Get("result")),
		text:    strings.TrimSpace(q.Get("q")),
		limit:   defaultAuditPageSize,
	}

	// The result is validated against the closed list instead of being passed
	// through as is: it is the only filter with a fixed vocabulary, and this way
	// a made-up value does not silently return zero rows.
	switch f.result {
	case "", string(audit.ResultSuccess), string(audit.ResultFailure), string(audit.ResultDenied):
	default:
		return f, fmt.Errorf("invalid result: %s", f.result)
	}

	var err error
	if f.from, err = parseOptionalTime(q.Get("from")); err != nil {
		return f, fmt.Errorf("invalid 'from' date")
	}
	if f.to, err = parseOptionalTime(q.Get("to")); err != nil {
		return f, fmt.Errorf("invalid 'to' date")
	}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return f, fmt.Errorf("invalid limit")
		}
		f.limit = min(n, maxAuditPageSize)
	}

	// Cursor "ts,id": both parts together because ts is not unique (several
	// entries can share a millisecond) and paginating by ts alone would lose or
	// repeat rows at the boundary.
	if raw := q.Get("cursor"); raw != "" {
		parts := strings.SplitN(raw, ",", 2)
		if len(parts) != 2 {
			return f, fmt.Errorf("invalid cursor")
		}
		if f.cursorTS, err = time.Parse(time.RFC3339Nano, parts[0]); err != nil {
			return f, fmt.Errorf("invalid cursor")
		}
		if f.cursorID, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
			return f, fmt.Errorf("invalid cursor")
		}
	}

	return f, nil
}

func parseOptionalTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, v)
}

// buildAuditQuery assembles the SELECT with the active filters.
//
// KEYSET pagination, never OFFSET: audit_log is partitioned by month, and a
// large OFFSET forces reading and discarding every earlier row, walking through
// whole partitions. With `(ts, id) < (cursor)` every page costs the same.
func buildAuditQuery(f auditFilter, columns string, limit int) (string, []any) {
	var where []string
	var args []any

	add := func(cond string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}

	// ILIKE with wildcards for the free-text fields: whoever searches an audit
	// log rarely remembers the exact value.
	if f.actor != "" {
		add("actor_username ILIKE '%%' || $%d || '%%'", f.actor)
	}
	if f.action != "" {
		add("action ILIKE '%%' || $%d || '%%'", f.action)
	}
	if f.cluster != "" {
		add("cluster ILIKE '%%' || $%d || '%%'", f.cluster)
	}
	if f.result != "" {
		add("result = $%d", f.result)
	}
	if f.text != "" {
		add("detail::text ILIKE '%%' || $%d || '%%'", f.text)
	}
	if !f.from.IsZero() {
		add("ts >= $%d", f.from)
	}
	if !f.to.IsZero() {
		add("ts <= $%d", f.to)
	}
	if !f.cursorTS.IsZero() {
		args = append(args, f.cursorTS, f.cursorID)
		where = append(where, fmt.Sprintf("(ts, id) < ($%d, $%d)", len(args)-1, len(args)))
	}

	query := "SELECT " + columns + " FROM audit_log"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY ts DESC, id DESC LIMIT $%d", len(args))
	return query, args
}

const auditColumns = `id, ts, user_id, actor_username, host(ip), action, cluster, namespace, resource, result, detail`

func scanAuditRow(scan func(dest ...any) error) (auditEntry, error) {
	var e auditEntry
	var actor, ip, cluster, namespace, resource *string
	var detail []byte
	if err := scan(&e.ID, &e.TS, &e.UserID, &actor, &ip, &e.Action, &cluster, &namespace, &resource, &e.Result, &detail); err != nil {
		return auditEntry{}, err
	}
	e.ActorUsername = deref(actor)
	e.IP = deref(ip)
	e.Cluster = deref(cluster)
	e.Namespace = deref(namespace)
	e.Resource = deref(resource)
	e.Detail = detail
	return e, nil
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// List returns one page of audit entries, newest to oldest.
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	f, err := parseAuditFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// One extra row is requested to know whether there is a next page without
	// counting the total (an unbounded COUNT over a partitioned table is exactly
	// what keyset pagination avoids).
	query, args := buildAuditQuery(f, auditColumns, f.limit+1)
	rows, err := h.DB.Query(r.Context(), query, args...)
	if err != nil {
		h.Logger.Error("audit.list: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []auditEntry{}
	for rows.Next() {
		e, err := scanAuditRow(rows.Scan)
		if err != nil {
			h.Logger.Error("audit.list: failed to read row", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		h.Logger.Error("audit.list: iteration failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := auditResponse{Items: items}
	if len(items) > f.limit {
		resp.Items = items[:f.limit]
		last := resp.Items[len(resp.Items)-1]
		resp.NextCursor = fmt.Sprintf("%s,%d", last.TS.Format(time.RFC3339Nano), last.ID)
	}

	// Only the FIRST page is audited, not every cursor advance: querying the
	// audit log is a sensitive action and has to leave a trace, but also
	// recording every "load more" would fill the table with noise generated by
	// the very screen that reads it.
	if f.cursorTS.IsZero() {
		h.auditQuery(r, "audit.query", f)
	}

	writeJSON(w, http.StatusOK, resp)
}

// exportTruncatedNote is written as the last line of the CSV when the cap is
// reached. It goes inside the file itself and not in an HTTP header because the
// recipient is a person opening a spreadsheet, who will never see a header —
// and a silently truncated export reads as "this is everything".
const exportTruncatedNote = "# TRUNCATED: only the first %d records matching the filter were exported; narrow the date range to see them all"

// ExportCSV downloads the entries matching the current filter.
func (h *AuditHandler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	f, err := parseAuditFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	query, args := buildAuditQuery(f, auditColumns, maxAuditExportRows+1)
	rows, err := h.DB.Query(r.Context(), query, args...)
	if err != nil {
		h.Logger.Error("audit.export: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="bayplan-audit.csv"`)

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"ts", "actor", "user_id", "ip", "action", "cluster", "namespace", "resource", "result", "detail"})

	count := 0
	truncated := false
	for rows.Next() {
		if count == maxAuditExportRows {
			truncated = true
			break
		}
		e, err := scanAuditRow(rows.Scan)
		if err != nil {
			h.Logger.Error("audit.export: failed to read row", "error", err)
			break
		}
		userID := ""
		if e.UserID != nil {
			userID = strconv.FormatInt(*e.UserID, 10)
		}
		_ = cw.Write(csvSafeRow([]string{
			e.TS.Format(time.RFC3339), e.ActorUsername, userID, e.IP, e.Action,
			e.Cluster, e.Namespace, e.Resource, e.Result, string(e.Detail),
		}))
		count++
	}
	if truncated {
		_ = cw.Write([]string{fmt.Sprintf(exportTruncatedNote, maxAuditExportRows)})
	}
	cw.Flush()

	// The export is ALWAYS audited, even though the query before it is not:
	// taking the audit log out of the system into a file is exactly what must be
	// reconstructible afterwards.
	h.auditQuery(r, "audit.export", f)
}

func (h *AuditHandler) auditQuery(r *http.Request, action string, f auditFilter) {
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		return
	}
	detail := map[string]any{}
	for k, v := range map[string]string{
		"actor": f.actor, "action": f.action, "cluster": f.cluster,
		"result": f.result, "q": f.text,
	} {
		if v != "" {
			detail[k] = v
		}
	}
	if !f.from.IsZero() {
		detail["from"] = f.from.Format(time.RFC3339)
	}
	if !f.to.IsZero() {
		detail["to"] = f.to.Format(time.RFC3339)
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &sess.UserID, ActorUsername: sess.Username, IP: middleware.ClientIP(r),
		Action: action, Result: audit.ResultSuccess, Detail: detail,
	})
}

// csvSafeCell neutralizes spreadsheet formula injection: the audit log holds
// values chosen by anyone who can reach the API (a username tried at login, a
// resource name, the detail JSON), and a cell starting with = + - @ or a
// tab/CR is evaluated as a formula by Excel/LibreOffice when the admin opens
// the export. A leading single quote makes it literal text (OWASP's
// recommendation); the value is otherwise untouched.
func csvSafeCell(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

func csvSafeRow(row []string) []string {
	for i, v := range row {
		row[i] = csvSafeCell(v)
	}
	return row
}
