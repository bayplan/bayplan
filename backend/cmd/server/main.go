// Command server starts the Bayplan backend: it applies migrations, connects
// to Postgres, creates the initial admin if needed and exposes the HTTP API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/bayplan/bayplan/internal/agent"
	"github.com/bayplan/bayplan/internal/ai"
	"github.com/bayplan/bayplan/internal/api"
	"github.com/bayplan/bayplan/internal/auth"
	"github.com/bayplan/bayplan/internal/backup"
	"github.com/bayplan/bayplan/internal/clientip"
	"github.com/bayplan/bayplan/internal/cloud"
	"github.com/bayplan/bayplan/internal/config"
	"github.com/bayplan/bayplan/internal/crypto"
	"github.com/bayplan/bayplan/internal/db"
	"github.com/bayplan/bayplan/internal/dockerenv"
	"github.com/bayplan/bayplan/internal/embeddedpg"
	"github.com/bayplan/bayplan/internal/extensions"
	"github.com/bayplan/bayplan/internal/gitops"
	"github.com/bayplan/bayplan/internal/helm"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/kubectl"
	"github.com/bayplan/bayplan/internal/metrics"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/notify"
	"github.com/bayplan/bayplan/internal/oidc"
	"github.com/bayplan/bayplan/internal/registries"
	"github.com/bayplan/bayplan/internal/rotate"
	"github.com/bayplan/bayplan/internal/scan"
	"github.com/bayplan/bayplan/internal/session"
	"github.com/bayplan/bayplan/internal/settings"
	"github.com/bayplan/bayplan/internal/tlsserve"
	"github.com/bayplan/bayplan/internal/web"
	"github.com/bayplan/bayplan/internal/ws"
)

// graphGroupCacheTTL is how long the group resolution via Microsoft Graph
// (overage fallback) is memoized per user. It does not bound privilege
// staleness mid-session (see internal/oidc/groupcache.go), it only avoids
// repeating the Graph call within short windows.
const graphGroupCacheTTL = 15 * time.Minute

// version is set at build time with -ldflags "-X main.version=…" (CI and Containerfile).
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	// Only the server may CREATE the all-in-one key. restore deals with the
	// key itself (a fresh one would leave the restored credentials
	// undecryptable); backup does not need it (pg_dump, and the key is never
	// in the dump); reencrypt needs the existing one and must not invent one
	// on a volume that has none (it would silently leave a key file that the
	// next server start would then trust).
	sub := ""
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}
	switch sub {
	case "":
		if err := ensureEncryptionKey(cfg, logger, true); err != nil {
			return err
		}
	case "reencrypt":
		if err := ensureEncryptionKey(cfg, logger, false); err != nil {
			return err
		}
	}

	// Maintenance subcommands: same binary and same configuration as the
	// server, so the environment does not have to be repeated elsewhere.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reencrypt":
			return runReencrypt(cfg, logger)
		case "backup":
			return runBackup(cfg, logger, os.Args[2:])
		case "restore":
			return runRestore(cfg, logger, os.Args[2:])
		default:
			return fmt.Errorf("unknown subcommand %q (available: reencrypt, backup, restore)", os.Args[1])
		}
	}

	if err := clientip.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stopDB, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer stopDB()

	if err := db.WaitReady(ctx, cfg.DatabaseURL, 2*time.Minute, logger); err != nil {
		return err
	}
	logger.Info("applying migrations")
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("database connection established")

	if err := db.EnsureAuditLogPartitions(ctx, pool); err != nil {
		return err
	}
	// Best effort: an old usage row costs nothing but disk.
	if err := ai.Prune(ctx, pool); err != nil {
		logger.Warn("ai: failed pruning old usage", "error", err)
	}

	if err := auth.Bootstrap(ctx, pool, logger, cfg.BootstrapPassword); err != nil {
		return err
	}

	keyring, err := crypto.NewKeyring(cfg.EncryptionKeyActiveID, cfg.EncryptionKeys)
	if err != nil {
		return err
	}

	settings.ServerVersion = version
	settingsStore := settings.New(pool, keyring)
	if err := settingsStore.Load(ctx); err != nil {
		return err
	}
	if err := settings.SeedServerDefaults(ctx, settingsStore, cfg, logger); err != nil {
		return err
	}

	k8sManager := k8s.NewManager(pool, keyring, logger)
	// Cloud credentials: the Manager builds the rest.Config of EKS/AKS/GKE
	// clusters through here (provider token renewed on its own).
	cloudService := cloud.NewService(cloud.NewStore(pool, keyring))
	k8sManager.SetCloudConnector(cloudService)

	// Agent hub: when an agent connects, the Manager (re)connects its cluster
	// through the tunnel; when it leaves, it is marked down instantly.
	agentHub := agent.NewHub(pool, logger)
	k8sManager.SetAgentDialer(func(clusterID int64) (k8s.DialFunc, bool) {
		if !agentHub.Connected(clusterID) {
			return nil, false
		}
		return agentHub.Dialer(clusterID), true
	})
	// Docker/Podman hosts: same tunnel, another "manager" (no informers).
	dockerManager := dockerenv.NewManager(func(clusterID int64) (dockerenv.DialFunc, bool) {
		if !agentHub.Connected(clusterID) {
			return nil, false
		}
		return agentHub.Dialer(clusterID), true
	})
	agentHub.OnConnect = func(clusterID int64) {
		cfg, err := k8sManager.LoadCluster(context.Background(), clusterID)
		if err != nil {
			logger.Error("agent: failed to load cluster on connect", "cluster", clusterID, "error", err)
			return
		}
		if cfg.Kind == "docker" {
			if err := dockerManager.Connect(clusterID, cfg.DisplayName); err != nil {
				logger.Error("agent: failed to connect docker host", "cluster", cfg.DisplayName, "error", err)
			}
			return
		}
		if err := k8sManager.Connect(cfg); err != nil {
			logger.Error("agent: failed to connect cluster through tunnel", "cluster", cfg.DisplayName, "error", err)
		}
	}
	agentHub.OnDisconnect = func(clusterID int64) {
		k8sManager.MarkAgentOffline(clusterID)
		dockerManager.MarkOffline(clusterID)
	}

	if cfg.LocalCluster {
		if _, err := k8sManager.EnsureLocalCluster(ctx, cfg.LocalClusterName); err != nil {
			return err
		}
	}
	if err := k8sManager.LoadAll(ctx); err != nil {
		return err
	}

	sessions := &session.Manager{
		Pool:            pool,
		IdleTimeout:     cfg.SessionIdleTimeout,
		AbsoluteTimeout: cfg.SessionAbsoluteTimeout,
		CookieSecure:    cfg.CookieSecure,
	}

	// 5 login attempts per minute per IP, with a burst of 5. A
	// single-instance deployment (one instance per DB) makes this in-memory limit
	// effective without depending on shared state.
	loginLimiter := middleware.NewIPRateLimiter(rate.Every(12*time.Second), 5)
	// Per account, failures only: wider than the per-IP one because it is
	// shared by everyone typing that username, and its job is to stop a
	// distributed guess, not a user who mistypes twice.
	accountLimiter := middleware.NewAccountLimiter(30*time.Second, 20)

	webHandler, err := web.Handler()
	if err != nil {
		return err
	}

	// Built here (not inside NewRouter) so that appMetrics can read their
	// active counters on every scrape without coupling the metrics package to
	// ws beyond the LogStreamsSource/ExecSessionsSource interfaces.
	logsHandler := &ws.LogsHandler{DB: pool, Logger: logger, K8s: k8sManager}
	execHandler := &ws.ExecHandler{DB: pool, Logger: logger, K8s: k8sManager}
	appMetrics := metrics.New(k8sManager, logsHandler, execHandler)

	// OIDC SSO: the configuration lives in settings and is applied hot. The
	// legacy AZURE_* variables are migrated once to settings (see
	// settings.MigrateLegacyEntra) so as not to break existing deployments.
	if err := settings.MigrateLegacyEntra(ctx, settingsStore, cfg, logger); err != nil {
		return err
	}
	oidcManager := oidc.NewManager(settingsStore, logger)
	oidcManager.Reload(ctx)
	stateSigner, err := oidc.NewStateSigner()
	if err != nil {
		return err
	}
	oidcHandler := &api.OIDCHandler{
		DB:           pool,
		Sessions:     sessions,
		Logger:       logger,
		Manager:      oidcManager,
		Graph:        &http.Client{Timeout: 15 * time.Second}, // an unresponsive Graph must not hang the callback
		GroupCache:   oidc.NewGroupCache(graphGroupCacheTTL),
		StateSigner:  stateSigner,
		CookieSecure: cfg.CookieSecure,
		Metrics:      appMetrics,
		Settings:     settingsStore,
	}

	helmRepos, err := helm.NewRepos(pool, keyring)
	if err != nil {
		return err
	}
	helmHandler := &api.HelmHandler{DB: pool, Logger: logger, K8s: k8sManager, Repos: helmRepos, Client: helm.NewClient(helmRepos, logger)}

	gitopsRec, err := gitops.NewReconciler(pool, keyring, k8sManager, logger)
	if err != nil {
		return err
	}
	if err := gitopsRec.Start(ctx); err != nil {
		return err
	}
	gitopsHandler := &api.GitOpsHandler{DB: pool, Logger: logger, K8s: k8sManager, Rec: gitopsRec}

	cloudHandler := &api.CloudHandler{DB: pool, Logger: logger, Cloud: cloudService}

	// Notifications: evaluates the rules every minute over the informer cache
	// issues and each cluster's connection.
	notifyStore := notify.NewStore(pool, keyring)
	notifyEngine := notify.NewEngine(notifyStore, pool, k8sManager, settingsStore, logger)
	notifyEngine.Start(ctx)
	notifyHandler := &api.NotificationsHandler{DB: pool, Logger: logger, Store: notifyStore, Engine: notifyEngine}

	// Image registries: pull secrets per namespace, resynced every 15 min.
	registriesSvc := registries.NewService(registries.NewStore(pool, keyring), k8sManager, logger)
	registriesSvc.Start(ctx)
	extensionsHandler := &api.ExtensionsHandler{DB: pool, Logger: logger, Store: &extensions.Store{DB: pool}}
	registriesHandler := &api.RegistriesHandler{DB: pool, Logger: logger, K8s: k8sManager, Svc: registriesSvc}

	// Configuration backups to S3: scheduler every minute.
	backupSvc := backup.NewService(backup.NewStore(pool, keyring), k8sManager, logger)
	backupSvc.Start(ctx)
	// Backups of Bayplan's own database (pg_dump to the same S3 targets).
	serverBackups := backup.NewServerBackups(pool, backupSvc.Store, settingsStore, cfg.DatabaseURL, logger)
	serverBackups.Start(ctx)
	backupHandler := &api.BackupHandler{DB: pool, Logger: logger, K8s: k8sManager, Svc: backupSvc, Server: serverBackups, Settings: settingsStore}

	// Image scanning with Trivy (optional: without the binary it is disabled).
	// Images from private registries use the credentials of
	// internal/registries if the host matches.
	scanRunner := scan.New(cfg.TrivyPath)
	if !scanRunner.Available() {
		logger.Warn("trivy not found: image scanning is disabled (TRIVY_PATH)")
	}
	registryCreds := func(ctx context.Context, host string) (*scan.Credentials, bool) {
		list, err := registriesSvc.Store.List(ctx)
		if err != nil {
			return nil, false
		}
		for _, reg := range list {
			if reg.Server == host || (host == "docker.io" && (reg.Server == "index.docker.io" || reg.Server == "docker.io")) {
				full, err := registriesSvc.Store.Get(ctx, reg.ID)
				if err != nil {
					return nil, false
				}
				return &scan.Credentials{Username: full.Username, Password: full.Password, Insecure: full.InsecureSkipTLS}, true
			}
		}
		return nil, false
	}
	scanSvc := scan.NewService(scan.NewStore(pool), scanRunner, registryCreds, logger)
	scanSvc.Start(ctx)
	imagesHandler := &api.ImagesHandler{DB: pool, Logger: logger, K8s: k8sManager, Scan: scanSvc}
	dockerHandler := &api.DockerHandler{DB: pool, Logger: logger, K8s: k8sManager, Docker: dockerManager}
	kubectlRunner := kubectl.New(cfg.KubectlPath)
	if !kubectlRunner.Available() {
		logger.Warn("kubectl not found: the kubectl web shell is disabled (KUBECTL_PATH)")
	}
	kubectlWS := &ws.KubectlHandler{DB: pool, Logger: logger, K8s: k8sManager, Runner: kubectlRunner}
	dockerWS := &ws.DockerHandler{DB: pool, Logger: logger, K8s: k8sManager, Docker: dockerManager, LogsCounter: logsHandler, ExecCounter: execHandler}

	// HTTPS served by the binary (all-in-one, or a host without a proxy). Its
	// self-signed CA goes into the agent manifests so that agents verify the
	// server instead of skipping verification.
	var tlsProvider *tlsserve.Provider
	if cfg.TLSListenAddr != "" {
		tlsProvider, err = tlsserve.New(tlsserve.Options{
			CertFile: cfg.TLSCertFile, KeyFile: cfg.TLSKeyFile,
			ACMEDomains: cfg.ACMEDomains, ACMEEmail: cfg.ACMEEmail, ACMEDirectory: cfg.ACMEDirectory,
			DataDir: cfg.DataDir,
			ExtraNames: func() []string {
				// Called on handshakes: the settings cache answers, the
				// timeout only bounds a cold miss.
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var srv settings.Server
				if settingsStore.Get(ctx, settings.KeyServer, &srv) != nil {
					return nil
				}
				if u, err := url.Parse(srv.PublicURL); err == nil && u.Hostname() != "" {
					return []string{u.Hostname()}
				}
				return nil
			},
		}, logger)
		if err != nil {
			return err
		}
	}
	agentCA := func() []byte {
		if tlsProvider != nil && tlsProvider.CAPEM() != nil {
			return tlsProvider.CAPEM()
		}
		if cfg.AgentCAFile != "" {
			if pemData, err := os.ReadFile(cfg.AgentCAFile); err == nil {
				return pemData
			}
		}
		return nil
	}

	router := api.NewRouter(api.Deps{
		DB:             pool,
		Logger:         logger,
		Sessions:       sessions,
		LoginLimiter:   loginLimiter,
		AccountLimiter: accountLimiter,
		Health:         &api.HealthHandler{DB: pool, Logger: logger, Sources: k8sManager},
		Keyring:        keyring,
		K8sManager:     k8sManager,
		OIDC:           oidcHandler,
		LogsHandler:    logsHandler,
		ExecHandler:    execHandler,
		Metrics:        appMetrics,
		Settings:       settingsStore,
		Helm:           helmHandler,
		GitOps:         gitopsHandler,
		AgentHub:       agentHub,
		AgentAuth:      agentHub,
		Docker:         dockerHandler,
		Cloud:          cloudHandler,
		Notify:         notifyHandler,
		Registries:     registriesHandler,
		Backups:        backupHandler,
		Images:         imagesHandler,
		Extensions:     extensionsHandler,
		DockerWS:       dockerWS,
		KubectlWS:      kubectlWS,
		Web:            webHandler,
		AgentCA:        agentCA,
		TLSMode:        tlsMode(tlsProvider),
	})

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		// InstrumentHTTP wraps EVERYTHING (including /metrics, /healthz and
		// the static assets) so that requests are counted there too.
		Handler:           web.SecurityHeaders(appMetrics.InstrumentHTTP(router), false),
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout (WebSockets, logs -f) but idle keep-alives are
		// released: a client cannot pin a connection forever by never sending.
		IdleTimeout: 2 * time.Minute,
	}

	errCh := make(chan error, 2)
	var tlsSrv *http.Server
	if tlsProvider != nil {
		tlsSrv = &http.Server{
			Addr: cfg.TLSListenAddr,
			// HSTS only here: this listener is the one actually speaking TLS.
			Handler:           web.SecurityHeaders(appMetrics.InstrumentHTTP(router), true),
			TLSConfig:         tlsProvider.TLSConfig(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}
		// With HTTPS on, plain HTTP only redirects (the session cookie is
		// Secure and would never come back over it), except the probes and
		// the metrics scrape, which stay on HTTP.
		srv.Handler = redirectToHTTPS(cfg.TLSListenAddr, cfg.ACMEDomains, settingsStore, srv.Handler)
		go func() {
			logger.Info("HTTPS server listening", "addr", cfg.TLSListenAddr, "certificates", tlsProvider.Mode())
			if err := tlsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}()
	}
	go func() {
		logger.Info("HTTP server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, closing server")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if tlsSrv != nil {
		_ = tlsSrv.Shutdown(shutdownCtx)
	}
	return srv.Shutdown(shutdownCtx)
}

func tlsMode(p *tlsserve.Provider) string {
	if p == nil {
		return ""
	}
	return p.Mode()
}

// ensureEncryptionKey generates and keeps the key in the data volume in the
// all-in-one mode when the environment brings none. With an external database
// the key stays mandatory in the environment: there it is supposed to live
// apart from the database backup.
//
// With create=false (maintenance subcommands) the key file must already exist.
func ensureEncryptionKey(cfg *config.Config, logger *slog.Logger, create bool) error {
	if len(cfg.EncryptionKeys) > 0 || !cfg.Embedded {
		return nil
	}
	path := filepath.Join(cfg.DataDir, "encryption-key")
	if !create {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("no encryption key: %s does not exist (is this the data volume of an initialized instance?) and the environment brings no ENCRYPTION_KEY_*: %w", path, err)
		}
	}
	key, err := crypto.LoadOrCreateKeyFile(path)
	if err != nil {
		return err
	}
	cfg.EncryptionKeys = map[string]string{"V1": key}
	cfg.EncryptionKeyActiveID = "V1"
	logger.Info("using the encryption key from the data volume", "file", path)
	return nil
}

// openDatabase starts the embedded PostgreSQL in the all-in-one mode and fills
// cfg.DatabaseURL with it; with an external database it does nothing.
func openDatabase(ctx context.Context, cfg *config.Config, logger *slog.Logger) (stop func(), err error) {
	if !cfg.Embedded {
		return func() {}, nil
	}
	pg, err := embeddedpg.Start(ctx, cfg.DataDir, logger)
	if err != nil {
		return nil, err
	}
	cfg.DatabaseURL = pg.URL
	return func() {
		if err := pg.Stop(); err != nil {
			logger.Error("embedded postgres", "error", err)
		}
	}, nil
}

// redirectToHTTPS sends browsers from the HTTP listener to the HTTPS one: to
// the public URL if it is https, otherwise to the same host on the HTTPS port
// (what `docker run -p 8443:8443` gives).
//
// Without an https public URL the target comes from the Host header, which
// the client controls: it is only used when it is a plain IP literal or DNS
// name (no userinfo, path or odd characters that could turn the Location
// into another site) and, with ACME, one of the configured domains. A browser
// always sends the host it was pointed at, so this never sends anyone to a
// host they did not already ask for; a forged Host (cache poisoning through
// a proxy) gets a 400 instead of a redirect elsewhere.
func redirectToHTTPS(tlsAddr string, acmeDomains []string, store *settings.Store, next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(tlsAddr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			next.ServeHTTP(w, r)
			return
		}
		var srv settings.Server
		_ = store.Get(r.Context(), settings.KeyServer, &srv)
		base := strings.TrimRight(srv.PublicURL, "/")
		if !strings.HasPrefix(base, "https://") {
			host, ok := redirectHost(r.Host, acmeDomains)
			if !ok {
				http.Error(w, "this server only serves HTTPS: open it with https:// (or set the public URL in Settings)", http.StatusBadRequest)
				return
			}
			if port != "" && port != "443" {
				host = net.JoinHostPort(host, port)
			} else if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
			base = "https://" + host
		}
		// 308 keeps the method and body of an API POST.
		http.Redirect(w, r, base+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

// dnsName is a hostname made of DNS labels (letters, digits, hyphens; labels
// not starting or ending in a hyphen).
var dnsName = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*\.?$`)

// redirectHost extracts and validates the hostname of a Host header for the
// HTTP->HTTPS redirect (see redirectToHTTPS). Pure, so it is testable.
func redirectHost(hostHeader string, acmeDomains []string) (string, bool) {
	host := hostHeader
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" || len(host) > 253 {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), true
	}
	if !dnsName.MatchString(host) {
		return "", false
	}
	if len(acmeDomains) > 0 {
		for _, d := range acmeDomains {
			if strings.EqualFold(strings.TrimSuffix(host, "."), d) {
				return host, true
			}
		}
		return "", false
	}
	return host, true
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	// Subcommands log to stderr: `bayplan backup -` writes the dump to stdout,
	// and a log line in the middle would corrupt it.
	out := os.Stdout
	if len(os.Args) > 1 {
		out = os.Stderr
	}
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: lvl}))
}

// runReencrypt re-encrypts with the active key everything stored with
// previous keys (see internal/rotate). It runs with the server stopped or
// running: each table goes in a transaction with FOR UPDATE, and the server
// keeps decrypting with any key in the keyring. What must NOT happen is
// removing the old key from the environment before this command finishes
// with no skipped rows.
func runReencrypt(cfg *config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopDB, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer stopDB()
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	keyring, err := crypto.NewKeyring(cfg.EncryptionKeyActiveID, cfg.EncryptionKeys)
	if err != nil {
		return err
	}
	rep, err := rotate.Run(ctx, pool, keyring, logger)
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		return err
	}
	for table, n := range rep.Skipped {
		if n > 0 {
			return fmt.Errorf("%d rows of %s have a key the keyring does not know: do not remove any key from the environment", n, table)
		}
	}
	return nil
}

// runBackup writes a dump of the database to a file ("-" = stdout), the same
// one the UI downloads: `docker exec bayplan bayplan backup - > bayplan.dump`.
func runBackup(cfg *config.Config, logger *slog.Logger, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: bayplan backup <file|->")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopDB, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer stopDB()
	out := os.Stdout
	if args[0] != "-" {
		f, err := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	return backup.Dump(ctx, cfg.DatabaseURL, out)
}

// runRestore loads a dump into an EMPTY database, before the server is
// started on it. The encryption key has to be the one of the instance the
// dump came from; in all-in-one mode it is written to the volume if the
// volume has none yet, so that the next start does not generate another one.
func runRestore(cfg *config.Config, logger *slog.Logger, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: bayplan restore <file>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.Embedded {
		keyFile := filepath.Join(cfg.DataDir, "encryption-key")
		if _, err := os.Stat(keyFile); errors.Is(err, os.ErrNotExist) {
			key, ok := cfg.EncryptionKeys[cfg.EncryptionKeyActiveID]
			if !ok {
				return errors.New("restore: pass the encryption key of the backed-up instance (ENCRYPTION_KEY_V1 and ENCRYPTION_KEY_ACTIVE_ID=v1) or copy its encryption-key file into the volume: without it the restored credentials cannot be decrypted")
			}
			if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
				return err
			}
		}
	}
	stopDB, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer stopDB()
	if err := backup.Restore(ctx, cfg.DatabaseURL, args[0]); err != nil {
		return err
	}
	logger.Info("restore finished; start the server with the same encryption key")
	return nil
}
