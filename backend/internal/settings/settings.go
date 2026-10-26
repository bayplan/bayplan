// Package settings stores the instance configuration that is edited live from
// the UI (branding, public URL, identity provider…), as opposed to the startup
// configuration that stays in environment variables (internal/config: DB,
// encryption keys, port).
//
// Each section is a row of the settings table with a JSON document. The Store
// caches every section in memory and serves them without hitting the DB on
// each request: branding is requested on every page load and the OIDC provider
// on every login. The cache is invalidated on every Set, which assumes a
// single instance of the binary — the same assumption the in-memory login
// rate limiter already makes.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/crypto"
)

// Section keys. They are the value of settings.key.
const (
	KeyServer = "server"
	KeyOIDC   = "auth.oidc"
	KeySMTP   = "smtp"
	KeyCosts  = "costs"
	// KeyServerBackup is the schedule of the backups of Bayplan's own
	// database (internal/backup/server.go).
	KeyServerBackup = "serverBackup"
	KeyAI           = "ai"
)

// Server is the visible network configuration: the public URL is used to
// build the OIDC redirect URI and absolute links, instead of asking the
// operator for it through an environment variable.
type Server struct {
	PublicURL string `json:"publicURL"`
	// AgentImage is the agent image written into the onboarding manifest of a
	// cluster with the agent connector. Configurable because each customer
	// publishes the image in their own registry.
	AgentImage string `json:"agentImage"`
	// AgentCACert is the CA (PEM) agents must trust to verify the public URL
	// when it is not signed by a public CA: an ingress with a corporate CA or
	// cert-manager's self-signed issuer. Empty with the server's own
	// self-signed HTTPS: that CA is known and used automatically.
	AgentCACert string `json:"agentCACert"`
}

// ServerVersion is set by main with the binary's version ("dev" outside a
// tagged build). The default agent image carries the SAME version as the
// server: agent and server are published together on every tag.
var ServerVersion = "dev"

// AgentImageRepository is the repository of the agent image published by the
// project's CI.
const AgentImageRepository = "ghcr.io/bayplan/bayplan-agent"

// DefaultAgentImage returns the agent image of the same version as the
// server, or :latest if the server has no version (development).
func DefaultAgentImage() string {
	v := strings.TrimPrefix(ServerVersion, "v")
	if v == "" || v == "dev" {
		return AgentImageRepository + ":latest"
	}
	return AgentImageRepository + ":" + v
}

// EffectiveAgentImage returns the configured one or the default.
func (s Server) EffectiveAgentImage() string {
	if strings.TrimSpace(s.AgentImage) != "" {
		return strings.TrimSpace(s.AgentImage)
	}
	return DefaultAgentImage()
}

// ServerBackup schedules the pg_dump of Bayplan's own database to one of the
// S3 targets (backup_targets). Zero values = disabled.
type ServerBackup struct {
	Enabled       bool  `json:"enabled"`
	TargetID      int64 `json:"targetId"`
	IntervalHours int   `json:"intervalHours"`
	Keep          int   `json:"keep"`
}

// Costs are the unit prices the panel uses to estimate the cost per
// namespace: what one CPU core and one GiB of memory cost per hour, in
// whatever currency. They are instance-wide (one rate for every cluster):
// whoever has different rates per cluster is in dedicated cost tools' territory,
// not that of an allocation estimate. Zero = not configured, and the UI says
// so.
// AI is the LLM provider behind "Explain" (internal/ai). One per instance: the
// organization pays for the calls, so the admin picks the provider, the model
// and how much each person may use. The key is stored like any other secret
// and never travels back to the browser.
type AI struct {
	Enabled bool `json:"enabled"`
	// Provider: "openai" (any OpenAI-compatible endpoint: OpenAI, Azure OpenAI,
	// Ollama, vLLM, LiteLLM…), "anthropic" or "gemini".
	Provider string `json:"provider"`
	// BaseURL only applies to "openai"; empty = api.openai.com.
	BaseURL string `json:"baseURL"`
	Model   string `json:"model"`
	APIKey  Secret `json:"apiKey"`
	// MinRole on the cluster to use it: it sends cluster data to a third party
	// and spends money, so readonly is not the default.
	MinRole string `json:"minRole"`
	// DailyLimitPerUser caps the calls per person and day (0 = default).
	DailyLimitPerUser int `json:"dailyLimitPerUser"`
}

const DefaultAIDailyLimit = 30

// Ready reports whether Explain can be offered at all.
func (a AI) Ready() bool {
	return a.Enabled && a.Provider != "" && strings.TrimSpace(a.Model) != "" && (a.APIKey.IsSet() || a.Provider == "openai" && a.BaseURL != "")
}

func (a AI) EffectiveMinRole() string {
	if a.MinRole == "" {
		return "editor"
	}
	return a.MinRole
}

func (a AI) EffectiveDailyLimit() int {
	if a.DailyLimitPerUser <= 0 {
		return DefaultAIDailyLimit
	}
	return a.DailyLimitPerUser
}

type Costs struct {
	CPUCoreHour   float64 `json:"cpuCoreHour"`
	MemoryGiBHour float64 `json:"memoryGiBHour"`
	Currency      string  `json:"currency"`
}

// Configured reports whether there is at least one price.
func (c Costs) Configured() bool { return c.CPUCoreHour > 0 || c.MemoryGiBHour > 0 }

// SMTP is the outgoing mail server used by the email notification channels.
// It is instance-wide (a single server for every channel), which is why it
// lives here and not in each channel.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password Secret `json:"password"`
	From     string `json:"from"`
	// StartTLS: negotiate TLS over the plaintext port (587). With port 465
	// implicit TLS is used and this field is ignored.
	StartTLS bool `json:"startTLS"`
	// InsecureSkipVerify: do not verify the mail server's certificate (lab
	// only).
	InsecureSkipVerify bool `json:"insecureSkipVerify"`

	password string
}

// SetPassword keeps the plaintext password ONLY in memory for sending (it is
// not serialized: the field has no json tag and is private).
func (s *SMTP) SetPassword(pw string) { s.password = pw }

// PlainPassword returns the password set with SetPassword.
func (s SMTP) PlainPassword() string { return s.password }

// Configured reports whether a mail server is defined.
func (s SMTP) Configured() bool {
	return strings.TrimSpace(s.Host) != "" && strings.TrimSpace(s.From) != ""
}

// EffectivePort returns the configured port or the usual one according to StartTLS.
func (s SMTP) EffectivePort() int {
	if s.Port > 0 {
		return s.Port
	}
	if s.StartTLS {
		return 587
	}
	return 465
}

// Secret is a value encrypted with the keyring (AES-GCM), as stored in a
// section's JSON. It never travels to the frontend: the API replaces it with
// an "is configured" boolean.
type Secret struct {
	KeyID      crypto.KeyID `json:"keyId"`
	Ciphertext []byte       `json:"ciphertext"`
}

// IsSet reports whether an encrypted value is stored.
func (s Secret) IsSet() bool { return len(s.Ciphertext) > 0 }

// Store reads and writes sections with an in-memory cache.
type Store struct {
	pool    *pgxpool.Pool
	keyring *crypto.Keyring

	mu    sync.RWMutex
	cache map[string]json.RawMessage
	subs  []func(key string)
}

func New(pool *pgxpool.Pool, keyring *crypto.Keyring) *Store {
	return &Store{pool: pool, keyring: keyring, cache: make(map[string]json.RawMessage)}
}

// Load preloads every section. It is called once at startup so that the first
// login or the first page load do not pay for the query.
func (s *Store) Load(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return fmt.Errorf("settings: failed to load: %w", err)
	}
	defer rows.Close()
	cache := make(map[string]json.RawMessage)
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Errorf("settings: failed to read row: %w", err)
		}
		cache[key] = json.RawMessage(value)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("settings: failed to iterate: %w", err)
	}
	s.mu.Lock()
	s.cache = cache
	s.mu.Unlock()
	return nil
}

// Subscribe registers a function invoked after every Set with the changed
// key. The OIDC provider uses it to rebuild itself without restarting.
func (s *Store) Subscribe(fn func(key string)) {
	s.mu.Lock()
	s.subs = append(s.subs, fn)
	s.mu.Unlock()
}

// Get decodes the section into out. A section that does not exist leaves out
// at its zero value and is NOT an error: the absence of configuration is a
// normal state (freshly installed instance).
func (s *Store) Get(ctx context.Context, key string, out any) error {
	s.mu.RLock()
	raw, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		var value []byte
		err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&value)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("settings: failed to read %q: %w", key, err)
		}
		raw = json.RawMessage(value)
		s.mu.Lock()
		s.cache[key] = raw
		s.mu.Unlock()
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("settings: %q has invalid JSON: %w", key, err)
	}
	return nil
}

// Set serializes v, stores it (upsert) and updates the cache. actor is the
// username making the change, for settings.updated_by.
func (s *Store) Set(ctx context.Context, key string, v any, actor string) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("settings: failed to serialize %q: %w", key, err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO settings (key, value, updated_by) VALUES ($1, $2, $3)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		key, raw, actor,
	)
	if err != nil {
		return fmt.Errorf("settings: failed to store %q: %w", key, err)
	}
	s.mu.Lock()
	s.cache[key] = json.RawMessage(raw)
	subs := append([]func(string){}, s.subs...)
	s.mu.Unlock()
	for _, fn := range subs {
		fn(key)
	}
	return nil
}

// EncryptSecret encrypts a value with the keyring's active key.
func (s *Store) EncryptSecret(plain string) (Secret, error) {
	keyID, ct, err := s.keyring.Encrypt([]byte(plain))
	if err != nil {
		return Secret{}, err
	}
	return Secret{KeyID: keyID, Ciphertext: ct}, nil
}

// DecryptSecret decrypts a stored Secret. An empty Secret returns "".
func (s *Store) DecryptSecret(sec Secret) (string, error) {
	if !sec.IsSet() {
		return "", nil
	}
	plain, err := s.keyring.Decrypt(sec.KeyID, sec.Ciphertext)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
