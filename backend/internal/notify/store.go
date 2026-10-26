package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/crypto"
)

var (
	ErrNotFound  = errors.New("notify: not found")
	ErrDuplicate = errors.New("notify: one with that name already exists")
	// ErrClusterNotFound: the rule points at a cluster id that does not exist (FK violation).
	ErrClusterNotFound = errors.New("notify: the specified cluster does not exist")
)

// Store persists channels, rules and the delivery history.
type Store struct {
	pool    *pgxpool.Pool
	keyring *crypto.Keyring
}

func NewStore(pool *pgxpool.Pool, keyring *crypto.Keyring) *Store {
	return &Store{pool: pool, keyring: keyring}
}

const channelColumns = `id, name, type, config, secret_encrypted, key_id, enabled, created_at, updated_at`

func (s *Store) scanChannel(row pgx.Row, withSecret bool) (Channel, error) {
	var c Channel
	var cfg, sec []byte
	var keyID *string
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &cfg, &sec, &keyID, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return Channel{}, err
	}
	if err := json.Unmarshal(cfg, &c.Config); err != nil {
		return Channel{}, fmt.Errorf("notify: corrupt config in channel %d: %w", c.ID, err)
	}
	c.SecretSet = len(sec) > 0
	if withSecret && len(sec) > 0 && keyID != nil {
		plain, err := s.keyring.Decrypt(crypto.KeyID(*keyID), sec)
		if err != nil {
			return Channel{}, fmt.Errorf("notify: failed decrypting channel %q: %w", c.Name, err)
		}
		if err := json.Unmarshal(plain, &c.Secret); err != nil {
			return Channel{}, fmt.Errorf("notify: corrupt secret in channel %d: %w", c.ID, err)
		}
	}
	return c, nil
}

// Channels lists every channel WITHOUT its secret.
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	return s.channels(ctx, `SELECT `+channelColumns+` FROM notification_channels ORDER BY name`, false)
}

// EnabledChannels returns the enabled channels WITH their secret, for sending.
func (s *Store) EnabledChannels(ctx context.Context) (map[int64]Channel, error) {
	list, err := s.channels(ctx, `SELECT `+channelColumns+` FROM notification_channels WHERE enabled ORDER BY name`, true)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]Channel, len(list))
	for _, c := range list {
		out[c.ID] = c
	}
	return out, nil
}

func (s *Store) channels(ctx context.Context, q string, withSecret bool) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := s.scanChannel(rows, withSecret)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Channel loads a channel WITH its secret.
func (s *Store) Channel(ctx context.Context, id int64) (*Channel, error) {
	c, err := s.scanChannel(s.pool.QueryRow(ctx, `SELECT `+channelColumns+` FROM notification_channels WHERE id = $1`, id), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) encryptSecret(sec ChannelSecret) ([]byte, *string, error) {
	if sec.URL == "" && sec.HeaderValue == "" {
		return nil, nil, nil
	}
	plain, err := json.Marshal(sec)
	if err != nil {
		return nil, nil, err
	}
	keyID, enc, err := s.keyring.Encrypt(plain)
	if err != nil {
		return nil, nil, err
	}
	k := string(keyID)
	return enc, &k, nil
}

func (s *Store) CreateChannel(ctx context.Context, c *Channel) (int64, error) {
	enc, keyID, err := s.encryptSecret(c.Secret)
	if err != nil {
		return 0, err
	}
	cfg, _ := json.Marshal(c.Config)
	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO notification_channels (name, type, config, secret_encrypted, key_id, enabled) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		c.Name, string(c.Type), cfg, enc, keyID, c.Enabled).Scan(&id)
	if isUniqueViolation(err) {
		return 0, ErrDuplicate
	}
	return id, err
}

func (s *Store) UpdateChannel(ctx context.Context, c *Channel) error {
	enc, keyID, err := s.encryptSecret(c.Secret)
	if err != nil {
		return err
	}
	cfg, _ := json.Marshal(c.Config)
	tag, err := s.pool.Exec(ctx,
		`UPDATE notification_channels SET name = $2, config = $3, secret_encrypted = $4, key_id = $5, enabled = $6, updated_at = now() WHERE id = $1`,
		c.ID, c.Name, cfg, enc, keyID, c.Enabled)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteChannel removes the channel and takes it out of the rules that used it
// (in the same transaction: channel_ids is not a FK).
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// The jsonb `-` operator with an integer removes by INDEX, not by value:
	// the array is rebuilt without the id.
	if _, err := tx.Exec(ctx,
		`UPDATE notification_rules
		    SET channel_ids = (SELECT COALESCE(jsonb_agg(x), '[]'::jsonb) FROM jsonb_array_elements(channel_ids) x WHERE x <> to_jsonb($1::bigint)),
		        updated_at = now()
		  WHERE channel_ids @> to_jsonb($1::bigint)`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const ruleColumns = `id, name, enabled, cluster_id, severities, kinds, reasons, namespaces, min_duration_seconds, cooldown_seconds, notify_resolved, channel_ids, created_at, updated_at`

func scanRule(row pgx.Row) (Rule, error) {
	var r Rule
	var sev, kinds, reasons, ns, ch []byte
	var minD, cool int
	if err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.ClusterID, &sev, &kinds, &reasons, &ns, &minD, &cool, &r.NotifyResolved, &ch, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Rule{}, err
	}
	_ = json.Unmarshal(sev, &r.Severities)
	_ = json.Unmarshal(kinds, &r.Kinds)
	_ = json.Unmarshal(reasons, &r.Reasons)
	_ = json.Unmarshal(ns, &r.Namespaces)
	_ = json.Unmarshal(ch, &r.ChannelIDs)
	r.MinDuration = time.Duration(minD) * time.Second
	r.Cooldown = time.Duration(cool) * time.Second
	return r, nil
}

func (s *Store) Rules(ctx context.Context, onlyEnabled bool) ([]Rule, error) {
	q := `SELECT ` + ruleColumns + ` FROM notification_rules`
	if onlyEnabled {
		q += ` WHERE enabled`
	}
	rows, err := s.pool.Query(ctx, q+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Rule(ctx context.Context, id int64) (*Rule, error) {
	r, err := scanRule(s.pool.QueryRow(ctx, `SELECT `+ruleColumns+` FROM notification_rules WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func ruleArgs(r *Rule) []any {
	sev, _ := json.Marshal(emptyIfNil(r.Severities))
	kinds, _ := json.Marshal(emptyIfNil(r.Kinds))
	reasons, _ := json.Marshal(emptyIfNil(r.Reasons))
	ns, _ := json.Marshal(emptyIfNil(r.Namespaces))
	ch, _ := json.Marshal(r.ChannelIDs)
	return []any{r.Name, r.Enabled, r.ClusterID, sev, kinds, reasons, ns, int(r.MinDuration / time.Second), int(r.Cooldown / time.Second), r.NotifyResolved, ch}
}

func emptyIfNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Store) CreateRule(ctx context.Context, r *Rule) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO notification_rules (name, enabled, cluster_id, severities, kinds, reasons, namespaces, min_duration_seconds, cooldown_seconds, notify_resolved, channel_ids)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`, ruleArgs(r)...).Scan(&id)
	if isUniqueViolation(err) {
		return 0, ErrDuplicate
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return 0, ErrClusterNotFound
	}
	return id, err
}

func (s *Store) UpdateRule(ctx context.Context, r *Rule) error {
	args := append([]any{r.ID}, ruleArgs(r)...)
	tag, err := s.pool.Exec(ctx,
		`UPDATE notification_rules SET name = $2, enabled = $3, cluster_id = $4, severities = $5, kinds = $6, reasons = $7, namespaces = $8,
		   min_duration_seconds = $9, cooldown_seconds = $10, notify_resolved = $11, channel_ids = $12, updated_at = now() WHERE id = $1`, args...)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification_rules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordDelivery stores the result of a send.
func (s *Store) RecordDelivery(ctx context.Context, d Delivery) error {
	var errText *string
	if d.Error != "" {
		e := d.Error
		if len(e) > 2000 {
			e = e[:2000]
		}
		errText = &e
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO notification_deliveries (rule_id, rule_name, channel_id, channel_name, cluster, issue_key, event, title, ok, error)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		d.RuleID, d.RuleName, d.ChannelID, d.ChannelName, d.Cluster, d.IssueKey, string(d.Event), d.Title, d.OK, errText)
	return err
}

// Deliveries returns the latest deliveries.
func (s *Store) Deliveries(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, ts, rule_id, rule_name, channel_id, channel_name, cluster, issue_key, event, title, ok, COALESCE(error, '')
		 FROM notification_deliveries ORDER BY ts DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.TS, &d.RuleID, &d.RuleName, &d.ChannelID, &d.ChannelName, &d.Cluster, &d.IssueKey, &d.Event, &d.Title, &d.OK, &d.Error); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Prune deletes history older than `keep`. It is not auditing: the audit_log is
// the one that is never touched.
func (s *Store) Prune(ctx context.Context, keep time.Duration) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notification_deliveries WHERE ts < now() - $1::interval`, fmt.Sprintf("%d seconds", int(keep/time.Second)))
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// StateRow is the persisted state of a problem for one rule (see migration
// 0024).
type StateRow struct {
	IssueKey  string
	RuleID    int64
	ClusterID int64
	FirstSeen time.Time
	LastSent  time.Time
	Sent      bool
}

// LoadState returns all the stored state. Called once at startup.
func (s *Store) LoadState(ctx context.Context) ([]StateRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT issue_key, rule_id, cluster_id, first_seen, last_sent, sent FROM notification_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StateRow
	for rows.Next() {
		var r StateRow
		if err := rows.Scan(&r.IssueKey, &r.RuleID, &r.ClusterID, &r.FirstSeen, &r.LastSent, &r.Sent); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveState applies in one transaction the changes of a tick: upsert of the
// live rows and deletion of the resolved problems. A deleted rule takes its
// rows with it through ON DELETE CASCADE; an upsert against an already deleted
// rule is ignored (ON CONFLICT does not cover the FK, hence the EXISTS).
func (s *Store) SaveState(ctx context.Context, upserts []StateRow, resolved []string) error {
	if len(upserts) == 0 && len(resolved) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, r := range upserts {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_state (issue_key, rule_id, cluster_id, first_seen, last_sent, sent)
			SELECT $1, $2, $3, $4, $5, $6 WHERE EXISTS (SELECT 1 FROM notification_rules WHERE id = $2)
			ON CONFLICT (issue_key, rule_id) DO UPDATE SET last_sent = EXCLUDED.last_sent, sent = EXCLUDED.sent, first_seen = EXCLUDED.first_seen`,
			r.IssueKey, r.RuleID, r.ClusterID, r.FirstSeen, r.LastSent, r.Sent); err != nil {
			return err
		}
	}
	if len(resolved) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM notification_state WHERE issue_key = ANY($1)`, resolved); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
