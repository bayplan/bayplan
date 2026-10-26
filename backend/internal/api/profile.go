package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/middleware"
)

// errInvalidBody is returned verbatim to the client, so it says what is wrong
// without leaking parsing details.
var errInvalidBody = errors.New("invalid request body: a JSON object is expected")

// maxProfileFieldLen bounds name and email. It is not format validation (the
// email is deliberately not validated: no mail is sent here, it is a contact
// datum the user writes themselves), it only prevents someone from stuffing a
// megabyte.
const maxProfileFieldLen = 200

// maxPreferencesBytes bounds the preferences document. It is a JSONB that
// comes from the client and is stored as is, so it needs a cap: without it,
// the users table is a store of arbitrary data of whatever size the browser
// wants.
const maxPreferencesBytes = 4096

// UpdateProfile modifies the profile data of the session's user.
//
// Blocked for SSO users: their name and email are sent by the IdP on every
// login (see upsertSSOUser), so allowing them to be edited here would give a
// field that overwrites itself on the next sign-in. Same criterion as Grafana,
// which disables the password change for users of an external provider.
func (h *AuthHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireCookieSession(w, r)
	if !ok {
		return
	}

	var body struct {
		DisplayName *string `json:"displayName"`
		Email       *string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var source string
	if err := h.DB.QueryRow(r.Context(), `SELECT source FROM users WHERE id = $1`, sess.UserID).Scan(&source); err != nil {
		h.Logger.Error("profile.update: failed reading user", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if source != "local" {
		h.auditProfile(r, sess.UserID, sess.Username, audit.ResultDenied, map[string]any{"reason": "not_local"})
		http.Error(w, "the profile of an SSO user is managed by the identity provider", http.StatusConflict)
		return
	}

	// nil pointers = "do not touch this field"; empty string = "clear it".
	// Without that distinction there would be no way to remove an email already
	// set.
	displayName := normalizeProfileField(body.DisplayName)
	email := normalizeProfileField(body.Email)
	if (displayName != nil && len(*displayName) > maxProfileFieldLen) ||
		(email != nil && len(*email) > maxProfileFieldLen) {
		http.Error(w, "value too long", http.StatusBadRequest)
		return
	}

	if _, err := h.DB.Exec(r.Context(),
		`UPDATE users
		 SET display_name = COALESCE($2, display_name),
		     email = COALESCE($3, email),
		     updated_at = now()
		 WHERE id = $1`,
		sess.UserID, displayName, email,
	); err != nil {
		h.Logger.Error("profile.update: update failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.auditProfile(r, sess.UserID, sess.Username, audit.ResultSuccess, nil)

	resp, err := h.currentUserResponse(r, sess.UserID, sess.Username, sess.MustChangePassword)
	if err != nil {
		h.Logger.Error("profile.update: failed composing response", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdatePreferences does a PARTIAL merge of the preferences document.
//
// Partial and not a replacement (Grafana's pattern): that way the frontend can
// save only what changed without dragging the rest along, and an old version
// of the UI does not wipe preferences it does not know about.
//
// Not audited: these are the user's own interface settings, with no security
// consequence, and auditing them would fill audit_log with noise. For the same
// reason migration 0007 removes `preferences` from the column list of
// trg_audit_db_users.
func (h *AuthHandler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	sess, ok := middleware.SessionFromContext(r.Context())
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	body, err := readLimitedJSONObject(r, maxPreferencesBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// The merge is done by Postgres with `||`, which at the top level of keys
	// is exactly the behavior wanted: new keys come in, existing ones are
	// overwritten and the rest stays.
	var updated []byte
	if err := h.DB.QueryRow(r.Context(),
		`UPDATE users SET preferences = preferences || $2::jsonb, updated_at = now()
		 WHERE id = $1
		 RETURNING preferences`,
		sess.UserID, body,
	).Scan(&updated); err != nil {
		h.Logger.Error("preferences.update: update failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, json.RawMessage(updated))
}

func (h *AuthHandler) auditProfile(r *http.Request, userID int64, username string, result audit.Result, detail map[string]any) {
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{
		UserID: &userID, ActorUsername: username, IP: middleware.ClientIP(r),
		Action: "profile.update", Result: result, Detail: detail,
	})
}

func normalizeProfileField(v *string) *string {
	if v == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	return &trimmed
}

// readLimitedJSONObject reads the body with a byte cap and checks that it is a
// JSON object. Returning it as raw bytes (and not deserialized into a struct)
// is what lets the backend not have to know the preferences.
func readLimitedJSONObject(r *http.Request, maxBytes int64) (json.RawMessage, error) {
	var raw json.RawMessage
	// MaxBytesReader cuts the read at the limit instead of reading everything
	// and measuring afterwards, which is what turns the cap into a real
	// defense.
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBytes)).Decode(&raw); err != nil {
		return nil, errInvalidBody
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, errInvalidBody
	}
	return raw, nil
}
