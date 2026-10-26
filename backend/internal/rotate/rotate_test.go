package rotate

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bayplan/bayplan/internal/crypto"
)

func keyring(t *testing.T, active string) *crypto.Keyring {
	t.Helper()
	k1 := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	k2 := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	kr, err := crypto.NewKeyring(active, map[string]string{"v1": k1, "v2": k2})
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func TestRewriteSecrets(t *testing.T) {
	old := keyring(t, "v1")
	_, ct, err := old.Encrypt([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{
		"host":      "smtp.example",
		"password":  map[string]any{"keyId": "v1", "ciphertext": base64.StdEncoding.EncodeToString(ct)},
		"nested":    []any{map[string]any{"keyId": "v1", "ciphertext": base64.StdEncoding.EncodeToString(ct)}},
		"lookalike": map[string]any{"keyId": "v1", "ciphertext": "x", "extra": true},
		"empty":     map[string]any{"keyId": "", "ciphertext": ""},
	}
	// Same document after a round trip through JSON, as it comes out of the DB.
	b, _ := json.Marshal(raw)
	var doc any
	_ = json.Unmarshal(b, &doc)

	kr := keyring(t, "v2")
	changed, skipped, err := RewriteSecrets(doc, kr)
	if err != nil || changed != 2 || skipped != 0 {
		t.Fatalf("changed=%d skipped=%d err=%v", changed, skipped, err)
	}
	m := doc.(map[string]any)
	pw := m["password"].(map[string]any)
	if pw["keyId"] != "v2" {
		t.Fatalf("keyId not rotated: %v", pw["keyId"])
	}
	ct2, _ := base64.StdEncoding.DecodeString(pw["ciphertext"].(string))
	plain, err := kr.Decrypt("v2", ct2)
	if err != nil || string(plain) != "hunter2" {
		t.Fatalf("does not decrypt with v2: %v %q", err, plain)
	}
	if m["lookalike"].(map[string]any)["keyId"] != "v1" {
		t.Fatal("touched an object that is not a Secret")
	}
	// Second pass: idempotent.
	changed, _, _ = RewriteSecrets(doc, kr)
	if changed != 0 {
		t.Fatalf("second pass changed %d", changed)
	}
}

func TestRewriteSecretsUnknownKey(t *testing.T) {
	kr := keyring(t, "v2")
	doc := map[string]any{"s": map[string]any{"keyId": "v9", "ciphertext": base64.StdEncoding.EncodeToString([]byte("zz"))}}
	changed, skipped, err := RewriteSecrets(doc, kr)
	if err != nil || changed != 0 || skipped != 1 {
		t.Fatalf("changed=%d skipped=%d err=%v", changed, skipped, err)
	}
}

// TestTablesCoverSchema: every *_encrypted column in the migrations must be
// in Tables, or the rotation would leave it with the old key.
func TestTablesCoverSchema(t *testing.T) {
	files, _ := filepath.Glob("../db/migrations/*.sql")
	if len(files) == 0 {
		t.Skip("no migrations at hand")
	}
	re := regexp.MustCompile(`(?m)^\s*(?:ALTER TABLE (\w+) ADD COLUMN )?(\w+_encrypted)\s+BYTEA`)
	tableRe := regexp.MustCompile(`(?m)^CREATE TABLE (\w+)`)
	want := map[string]bool{}
	for _, f := range files {
		data, _ := os.ReadFile(f)
		s := string(data)
		// Column inside a CREATE TABLE: the table is the last CREATE TABLE
		// before the line.
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			col := s[m[4]:m[5]]
			table := ""
			if m[2] >= 0 {
				table = s[m[2]:m[3]]
			} else {
				prev := tableRe.FindAllStringSubmatchIndex(s[:m[0]], -1)
				if len(prev) == 0 {
					continue
				}
				last := prev[len(prev)-1]
				table = s[last[2]:last[3]]
			}
			if strings.Contains(s[:m[0]], "+goose Down") && strings.LastIndex(s[:m[0]], "+goose Down") > strings.LastIndex(s[:m[0]], "+goose Up") {
				continue
			}
			want[table+"."+col] = true
		}
	}
	have := map[string]bool{}
	for _, tb := range Tables {
		for _, c := range tb.ciphers {
			have[tb.name+"."+c] = true
		}
	}
	for k := range want {
		if !have[k] {
			t.Errorf("encrypted column %s is not in rotate.Tables", k)
		}
	}
}
