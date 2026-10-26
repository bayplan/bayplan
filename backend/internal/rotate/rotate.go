// Package rotate re-encrypts with the keyring's active key everything stored
// with an earlier key. It is the second step of a key rotation: first
// ENCRYPTION_KEY_V2 is added and ENCRYPTION_KEY_ACTIVE_ID is changed (new data
// is already encrypted with v2, old data is still decrypted with v1), and then
// `bayplan reencrypt` leaves EVERYTHING in v2 so that v1 can be removed from
// the environment. Without this command the old key could never be removed.
package rotate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/bayplan/bayplan/internal/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
)

// table describes a table with a key_id column and one or more encrypted
// columns that share that key version (in `clusters` the token and the client
// key go with the same key_id).
type table struct {
	name    string
	pk      string
	keyID   string
	ciphers []string
}

// Tables is the list of encrypted columns in the schema. When adding a new
// secret to the DB it must be added here, or the rotation will leave it with
// the old key; the `TestTablesCoverSchema` test compares against the
// migrations.
var Tables = []table{
	{name: "clusters", pk: "id", keyID: "key_id", ciphers: []string{"sa_token_encrypted", "client_key_encrypted"}},
	{name: "helm_repos", pk: "id", keyID: "key_id", ciphers: []string{"password_encrypted"}},
	{name: "gitops_sources", pk: "id", keyID: "key_id", ciphers: []string{"token_encrypted"}},
	{name: "gitops_sources", pk: "id", keyID: "webhook_key_id", ciphers: []string{"webhook_token_encrypted"}},
	{name: "cloud_credentials", pk: "id", keyID: "key_id", ciphers: []string{"secret_encrypted"}},
	{name: "notification_channels", pk: "id", keyID: "key_id", ciphers: []string{"secret_encrypted"}},
	{name: "image_registries", pk: "id", keyID: "key_id", ciphers: []string{"password_encrypted"}},
	{name: "backup_targets", pk: "id", keyID: "key_id", ciphers: []string{"secret_encrypted"}},
}

// Report counts what was re-encrypted per table (and "settings" for the sections).
type Report struct {
	Rows map[string]int `json:"rows"`
	// Skipped: rows with a key version the keyring does not know. It is not a
	// command error (the row was already unusable), but it must be known.
	Skipped map[string]int `json:"skipped"`
}

// Run re-encrypts everything not under the active key. Each table goes in its
// own transaction: if one fails, the previous ones stay done and the command
// can be re-run (it is idempotent: what is already re-encrypted does not match
// the WHERE).
func Run(ctx context.Context, pool *pgxpool.Pool, kr *crypto.Keyring, logger *slog.Logger) (Report, error) {
	rep := Report{Rows: map[string]int{}, Skipped: map[string]int{}}
	active := kr.ActiveID()
	for _, t := range Tables {
		n, skipped, err := rotateTable(ctx, pool, kr, t)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", t.name, err)
		}
		rep.Rows[t.name] += n
		rep.Skipped[t.name] += skipped
		logger.Info("reencrypt: table", "table", t.name, "reencrypted", n, "skipped", skipped)
	}
	n, skipped, err := rotateSettings(ctx, pool, kr)
	if err != nil {
		return rep, fmt.Errorf("settings: %w", err)
	}
	rep.Rows["settings"] = n
	rep.Skipped["settings"] = skipped
	logger.Info("reencrypt: settings", "reencrypted", n, "skipped", skipped, "key", active)
	return rep, nil
}

func rotateTable(ctx context.Context, pool *pgxpool.Pool, kr *crypto.Keyring, t table) (int, int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)

	cols := t.pk + ", " + t.keyID
	for _, c := range t.ciphers {
		cols += ", " + c
	}
	// FOR UPDATE: a concurrent write by the server (rotating a credential
	// while the command runs) waits instead of clobbering.
	rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s IS NOT NULL AND %s <> $1 FOR UPDATE", cols, t.name, t.keyID, t.keyID), kr.ActiveID())
	if err != nil {
		return 0, 0, err
	}
	type row struct {
		id      int64
		keyID   string
		ciphers [][]byte
	}
	var pending []row
	for rows.Next() {
		r := row{ciphers: make([][]byte, len(t.ciphers))}
		dest := []any{&r.id, &r.keyID}
		for i := range r.ciphers {
			dest = append(dest, &r.ciphers[i])
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return 0, 0, err
		}
		pending = append(pending, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	n, skipped := 0, 0
	for _, r := range pending {
		set := ""
		args := []any{r.id, kr.ActiveID()}
		ok := true
		for i, c := range t.ciphers {
			if r.ciphers[i] == nil {
				continue
			}
			plain, err := kr.Decrypt(r.keyID, r.ciphers[i])
			if err != nil {
				ok = false
				break
			}
			_, enc, err := kr.Encrypt(plain)
			if err != nil {
				return n, skipped, err
			}
			args = append(args, enc)
			set += fmt.Sprintf(", %s = $%d", c, len(args))
		}
		if !ok {
			skipped++
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s = $2%s WHERE %s = $1", t.name, t.keyID, set, t.pk), args...); err != nil {
			return n, skipped, err
		}
		n++
	}
	return n, skipped, tx.Commit(ctx)
}

// rotateSettings walks the JSON of every section looking for {keyId,
// ciphertext} objects (settings.Secret) with a key other than the active one.
func rotateSettings(ctx context.Context, pool *pgxpool.Pool, kr *crypto.Keyring) (int, int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT key, value FROM settings FOR UPDATE")
	if err != nil {
		return 0, 0, err
	}
	type section struct {
		key string
		raw []byte
	}
	var secs []section
	for rows.Next() {
		var s section
		if err := rows.Scan(&s.key, &s.raw); err != nil {
			rows.Close()
			return 0, 0, err
		}
		secs = append(secs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	n, skipped := 0, 0
	for _, s := range secs {
		var doc any
		if err := json.Unmarshal(s.raw, &doc); err != nil {
			return n, skipped, fmt.Errorf("section %q: %w", s.key, err)
		}
		changed, sk, err := RewriteSecrets(doc, kr)
		if err != nil {
			return n, skipped, fmt.Errorf("section %q: %w", s.key, err)
		}
		skipped += sk
		if changed == 0 {
			continue
		}
		out, err := json.Marshal(doc)
		if err != nil {
			return n, skipped, err
		}
		if _, err := tx.Exec(ctx, "UPDATE settings SET value = $2, updated_at = now() WHERE key = $1", s.key, out); err != nil {
			return n, skipped, err
		}
		n += changed
	}
	return n, skipped, tx.Commit(ctx)
}

// RewriteSecrets re-encrypts in place the {keyId, ciphertext} objects of a
// JSON document decoded into any. It returns how many it changed and how many
// it could not decrypt. It is pure (no DB) so it can be tested.
func RewriteSecrets(doc any, kr *crypto.Keyring) (changed, skipped int, err error) {
	switch v := doc.(type) {
	case map[string]any:
		if isSecret(v) {
			keyID, _ := v["keyId"].(string)
			if keyID == kr.ActiveID() || keyID == "" {
				return 0, 0, nil
			}
			ctB64, _ := v["ciphertext"].(string)
			ct, err := base64.StdEncoding.DecodeString(ctB64)
			if err != nil {
				return 0, 0, fmt.Errorf("ciphertext is not base64: %w", err)
			}
			if len(ct) == 0 {
				return 0, 0, nil
			}
			plain, err := kr.Decrypt(keyID, ct)
			if err != nil {
				return 0, 1, nil
			}
			newID, enc, err := kr.Encrypt(plain)
			if err != nil {
				return 0, 0, err
			}
			v["keyId"] = newID
			v["ciphertext"] = base64.StdEncoding.EncodeToString(enc)
			return 1, 0, nil
		}
		for _, child := range v {
			c, s, err := RewriteSecrets(child, kr)
			if err != nil {
				return changed, skipped, err
			}
			changed += c
			skipped += s
		}
	case []any:
		for _, child := range v {
			c, s, err := RewriteSecrets(child, kr)
			if err != nil {
				return changed, skipped, err
			}
			changed += c
			skipped += s
		}
	}
	return changed, skipped, nil
}

// isSecret recognizes the serialized form of settings.Secret: exactly the two
// keys. A user object with those same keys plus others is not touched.
func isSecret(m map[string]any) bool {
	if len(m) != 2 {
		return false
	}
	_, a := m["keyId"].(string)
	_, b := m["ciphertext"].(string)
	return a && b
}
