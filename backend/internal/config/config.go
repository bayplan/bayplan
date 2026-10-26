// Package config loads the irreducible bootstrap configuration from
// environment variables (DATABASE_URL, ENCRYPTION_KEY_ACTIVE_ID and
// ENCRYPTION_KEY_V<n>, LISTEN_ADDR, COOKIE_SECURE, TRUSTED_PROXIES, timeouts…);
// everything else lives in the settings table. It does not read secrets from
// versioned files: the sensitive values arrive through the environment from a
// Kubernetes Secret, a Podman/Compose secret or an env file outside the repo.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config groups the backend's whole startup configuration.
type Config struct {
	// HTTP
	ListenAddr string // HTTP server listen address, e.g. ":8080"

	// Database. Empty = all-in-one mode: the server starts the PostgreSQL
	// shipped in the image with its data under DataDir (internal/embeddedpg).
	DatabaseURL string
	Embedded    bool
	// DataDir is the container's volume (DATA_DIR, /data by default): embedded
	// database, generated encryption key, self-signed CA and ACME cache.
	DataDir string

	// HTTPS served by the binary itself (internal/tlsserve). Empty
	// TLSListenAddr = plain HTTP only, the usual case behind an ingress or a
	// proxy. The all-in-one mode turns it on (":8443") unless TLS_LISTEN_ADDR
	// is explicitly set, even to empty.
	TLSListenAddr string
	TLSCertFile   string
	TLSKeyFile    string
	ACMEDomains   []string
	ACMEEmail     string
	ACMEDirectory string
	// AgentCAFile is a CA (PEM) to put in the agent manifests when HTTPS is
	// terminated elsewhere with a private CA: the chart mounts the one of
	// cert-manager's self-signed chain here. Read on every manifest, so a
	// file that appears after startup is picked up.
	AgentCAFile string

	// BootstrapPassword is the initial admin password for unattended
	// installations (BOOTSTRAP_PASSWORD, the chart's bootstrapPassword). Only
	// used when the users table is empty, and the admin is forced to change
	// it on first login: the value left in a Secret or an env file stops
	// being a credential.
	BootstrapPassword string

	// Sessions
	SessionIdleTimeout     time.Duration // inactivity expiration (sliding)
	SessionAbsoluteTimeout time.Duration // maximum absolute expiration
	CookieSecure           bool          // force the Secure cookie flag (disable only in local dev without TLS)

	// Encryption of cluster credentials (AES-GCM). Must be 32 bytes after
	// base64 decoding (AES-256). Active version for new encryptions.
	EncryptionKeyActiveID string            // key_id of the active key
	EncryptionKeys        map[string]string // key_id -> base64 key (active + historical ones for decrypting)

	// Legacy Entra ID SSO. Only read to migrate it ONCE to settings
	// (settings.MigrateLegacyEntra); the live configuration is in the DB.
	AzureTenantID     string
	AzureClientID     string
	AzureClientSecret string
	AzureRedirectURI  string

	// Logging
	LogLevel string // debug|info|warn|error

	// LocalCluster: register the cluster the server runs in as "this
	// cluster" (incluster connector). Enabled by the chart; outside Kubernetes
	// it has no effect.
	LocalCluster     bool
	LocalClusterName string

	// TrustedProxies are the networks (CIDR or IP) from which X-Forwarded-For
	// is accepted. Empty = the header is always ignored. The documented
	// deployment has a TLS proxy in front, so without this the audit trail and
	// rate limiting would only see the proxy's IP.
	TrustedProxies []string

	// INITIAL values for settings (only applied if the section is empty in
	// the DB; afterwards whatever the admin edits in Settings wins). They let
	// a Helm chart or a compose file leave the instance ready without going
	// through the UI.
	PublicURL  string
	AgentImage string
	// KubectlPath is the kubectl binary for the web shell (KUBECTL_PATH);
	// empty looks up "kubectl" in the PATH. If it does not exist, the shell is
	// disabled.
	KubectlPath string
	// TrivyPath is the Trivy binary for image scanning (TRIVY_PATH); empty
	// looks up "trivy" in the PATH. If it does not exist, scanning is disabled.
	TrivyPath string
}

// Load builds the Config from environment variables. It returns an error if a
// required variable is missing or has an invalid format.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:   getEnvDefault("LISTEN_ADDR", ":8080"),
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		CookieSecure: getEnvBoolDefault("COOKIE_SECURE", true),
		// Normalized to uppercase: it must match the suffix of
		// ENCRYPTION_KEY_<ID> (see loadEncryptionKeys), and environment
		// variables are case-sensitive by operating system convention, so
		// without this "v1" and "V1" would never match.
		EncryptionKeyActiveID: strings.ToUpper(os.Getenv("ENCRYPTION_KEY_ACTIVE_ID")),
		AzureTenantID:         os.Getenv("AZURE_TENANT_ID"),
		AzureClientID:         os.Getenv("AZURE_CLIENT_ID"),
		AzureClientSecret:     os.Getenv("AZURE_CLIENT_SECRET"),
		AzureRedirectURI:      os.Getenv("AZURE_REDIRECT_URI"),
		LogLevel:              getEnvDefault("LOG_LEVEL", "info"),
		TrustedProxies:        splitList(os.Getenv("TRUSTED_PROXIES")),
		PublicURL:             strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_URL")), "/"),
		AgentImage:            strings.TrimSpace(os.Getenv("AGENT_IMAGE")),
		LocalCluster:          os.Getenv("LOCAL_CLUSTER") == "true",
		LocalClusterName:      strings.TrimSpace(os.Getenv("LOCAL_CLUSTER_NAME")),
		KubectlPath:           strings.TrimSpace(os.Getenv("KUBECTL_PATH")),
		TrivyPath:             strings.TrimSpace(os.Getenv("TRIVY_PATH")),
	}

	cfg.DataDir = getEnvDefault("DATA_DIR", "/data")
	cfg.Embedded = cfg.DatabaseURL == ""
	cfg.TLSListenAddr = os.Getenv("TLS_LISTEN_ADDR")
	if _, set := os.LookupEnv("TLS_LISTEN_ADDR"); !set && cfg.Embedded {
		cfg.TLSListenAddr = ":8443"
	}
	cfg.TLSCertFile = strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	cfg.TLSKeyFile = strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	cfg.ACMEDomains = splitList(os.Getenv("ACME_DOMAINS"))
	cfg.ACMEEmail = strings.TrimSpace(os.Getenv("ACME_EMAIL"))
	cfg.ACMEDirectory = strings.TrimSpace(os.Getenv("ACME_DIRECTORY"))
	if cfg.TLSListenAddr == "" && (cfg.TLSCertFile != "" || len(cfg.ACMEDomains) > 0) {
		return nil, fmt.Errorf("config: TLS_CERT_FILE/ACME_DOMAINS need TLS_LISTEN_ADDR (e.g. :8443)")
	}
	cfg.BootstrapPassword = os.Getenv("BOOTSTRAP_PASSWORD")
	cfg.AgentCAFile = strings.TrimSpace(os.Getenv("AGENT_CA_FILE"))

	idleTimeout, err := getEnvDurationDefault("SESSION_IDLE_TIMEOUT", 30*time.Minute)
	if err != nil {
		return nil, err
	}
	cfg.SessionIdleTimeout = idleTimeout

	absTimeout, err := getEnvDurationDefault("SESSION_ABSOLUTE_TIMEOUT", 12*time.Hour)
	if err != nil {
		return nil, err
	}
	cfg.SessionAbsoluteTimeout = absTimeout

	cfg.EncryptionKeys, err = loadEncryptionKeys()
	if err != nil {
		return nil, err
	}

	if err := cfg.validateEntra(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// EntraEnabled reports whether Entra ID SSO is configured. The total absence
// of the 4 AZURE_* variables is a valid state (SSO disabled; startup does not
// depend on already having an Azure tenant); validateEntra already guarantees
// there is no half-done configuration.
func (c *Config) EntraEnabled() bool {
	return c.AzureTenantID != "" && c.AzureClientID != "" && c.AzureClientSecret != "" && c.AzureRedirectURI != ""
}

// validateEntra requires the 4 AZURE_* variables to be either all present or
// all absent. A half-done configuration (e.g. missing client secret) would
// leave the SSO button visible but broken at login time, instead of failing
// fast and clearly at startup.
func (c *Config) validateEntra() error {
	present := 0
	for _, v := range []string{c.AzureTenantID, c.AzureClientID, c.AzureClientSecret, c.AzureRedirectURI} {
		if v != "" {
			present++
		}
	}
	if present > 0 && present < 4 {
		return fmt.Errorf("config: Entra ID SSO requires AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET and AZURE_REDIRECT_URI together (or none of them)")
	}
	return nil
}

// loadEncryptionKeys reads ENCRYPTION_KEY_<ID> from the environment. It allows
// keeping historical keys to decrypt old records after a rotation, even though
// only ENCRYPTION_KEY_ACTIVE_ID is used to encrypt new data.
// Expected format: ENCRYPTION_KEY_V1=<base64 32 bytes>, ENCRYPTION_KEY_V2=...
func loadEncryptionKeys() (map[string]string, error) {
	keys := map[string]string{}
	for _, e := range os.Environ() {
		const prefix = "ENCRYPTION_KEY_"
		if len(e) <= len(prefix) || e[:len(prefix)] != prefix {
			continue
		}
		// e has the form "ENCRYPTION_KEY_V1=value"
		rest := e[len(prefix):]
		for i := 0; i < len(rest); i++ {
			if rest[i] == '=' {
				name := strings.ToUpper(rest[:i])
				if name == "ACTIVE_ID" {
					break // already read as EncryptionKeyActiveID
				}
				keys[name] = rest[i+1:]
				break
			}
		}
	}
	return keys, nil
}

// splitList splits a comma-separated list, trimming and discarding empties.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBoolDefault(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getEnvDurationDefault(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s: %w", key, err)
	}
	return d, nil
}
