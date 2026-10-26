// Command agent is the agent deployed INSIDE a managed cluster that uses the
// agent connector. It opens an outbound WebSocket connection to the Bayplan
// server and keeps a tunnel (remotedialer) on top of it through which
// the server reaches the cluster's API server.
//
// The agent does not expose the API server as-is: inside the tunnel it only
// allows connections to its own local proxy (127.0.0.1:8081), which adds its
// ServiceAccount token to every request and forwards to the API server over
// TLS with the cluster CA. That way the server never sees the token, projected
// token rotation is transparent, and the only permission the agent needs is
// whatever the manifest's RBAC grants it (the same ClusterRole as the direct
// connector's technical SA, plus impersonation).
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rancher/remotedialer"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

const (
	proxyAddr = "127.0.0.1:8081"
	// defaultSADir is where the kubelet projects token and CA. BAYPLAN_SA_DIR
	// overrides it to run the agent outside a pod (tests, or a VM that reaches
	// the API server and has an exported token).
	defaultSADir   = "/var/run/secrets/kubernetes.io/serviceaccount"
	tokenCacheTTL  = time.Minute
	reconnectMin   = 2 * time.Second
	reconnectMax   = 60 * time.Second
	handshakeLimit = 15 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	serverURL := strings.TrimRight(os.Getenv("BAYPLAN_SERVER_URL"), "/")
	clusterID := os.Getenv("BAYPLAN_CLUSTER_ID")
	agentKey := os.Getenv("BAYPLAN_AGENT_KEY")
	if serverURL == "" || clusterID == "" || agentKey == "" {
		return errors.New("missing BAYPLAN_SERVER_URL, BAYPLAN_CLUSTER_ID or BAYPLAN_AGENT_KEY")
	}
	insecure := os.Getenv("BAYPLAN_INSECURE_SKIP_VERIFY") == "true"
	serverTLS, err := serverTLSConfig(os.Getenv("BAYPLAN_CA_CERT"), insecure)
	if err != nil {
		return err
	}

	// Two modes: Kubernetes (inside a pod: proxy to the API server with the SA
	// token) or Docker (on a host: proxy to the Docker/Podman socket). Chosen
	// by BAYPLAN_MODE or, failing that, by the environment: a pod has
	// KUBERNETES_SERVICE_HOST; a host has the socket.
	var proxy http.Handler
	var rbac *rbacSyncer
	mode := os.Getenv("BAYPLAN_MODE")
	if mode == "" {
		if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			mode = "kubernetes"
		} else {
			mode = "docker"
		}
	}
	switch mode {
	case "kubernetes":
		apiHost := os.Getenv("KUBERNETES_SERVICE_HOST")
		apiPort := os.Getenv("KUBERNETES_SERVICE_PORT")
		if apiHost == "" || apiPort == "" {
			return errors.New("kubernetes mode: missing KUBERNETES_SERVICE_HOST/PORT")
		}
		saDir := os.Getenv("BAYPLAN_SA_DIR")
		if saDir == "" {
			saDir = defaultSADir
		}
		p, err := newAPIProxy(net.JoinHostPort(apiHost, apiPort), saDir, logger)
		if err != nil {
			return err
		}
		proxy = p
		rbac = &rbacSyncer{
			serverURL: serverURL, clusterID: clusterID, agentKey: agentKey, apiHostPort: net.JoinHostPort(apiHost, apiPort),
			kube:  &http.Client{Transport: &http.Transport{TLSClientConfig: p.kubeTLS.Clone()}},
			token: p.currentToken, logger: logger.With("comp", "rbac"), currentImage: os.Getenv("BAYPLAN_IMAGE"),
		}
	case "docker":
		sock := os.Getenv("BAYPLAN_DOCKER_SOCKET")
		if sock == "" {
			sock = "/var/run/docker.sock"
		}
		if _, err := os.Stat(sock); err != nil {
			return fmt.Errorf("docker mode: cannot find socket %s (mount /var/run/docker.sock or set BAYPLAN_DOCKER_SOCKET): %w", sock, err)
		}
		proxy = newDockerProxy(sock, logger)
	default:
		return fmt.Errorf("unknown BAYPLAN_MODE: %q (kubernetes | docker)", mode)
	}
	logger.Info("mode", "mode", mode)
	ln, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", proxyAddr, err)
	}
	srv := &http.Server{Handler: proxy, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("local proxy down", "error", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// With unverified TLS towards the server the agent does NOT self-update: a
	// man in the middle could dictate its RBAC. The tunnel is still opened (it
	// is what the operator asked for in their lab), but the operator applies
	// the RBAC themselves.
	if rbac != nil && insecure {
		logger.Warn("rbac: self-update disabled because BAYPLAN_INSECURE_SKIP_VERIFY=true; reapply the manifest when upgrading the panel")
		rbac = nil
	}
	if rbac != nil {
		rbac.server = &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: serverTLS}}
		go rbac.run(ctx)
	}

	wsURL := strings.Replace(strings.Replace(serverURL, "https://", "wss://", 1), "http://", "ws://", 1) + "/api/agent/connect"
	headers := http.Header{}
	headers.Set("X-Bayplan-Cluster", clusterID)
	headers.Set("Authorization", "Bearer "+agentKey)
	headers.Set("X-Bayplan-Agent-Version", version)
	dialer := &websocket.Dialer{HandshakeTimeout: handshakeLimit, Proxy: http.ProxyFromEnvironment, TLSClientConfig: serverTLS}
	// Only dialing the local proxy is allowed: nothing else in the cluster is
	// reachable from the server even with the tunnel open.
	allow := func(proto, address string) bool { return proto == "tcp" && address == proxyAddr }

	backoff := reconnectMin
	for {
		logger.Info("connecting", "server", wsURL, "cluster", clusterID, "version", version)
		start := time.Now()
		err := remotedialer.ClientConnect(ctx, wsURL, headers, dialer, allow, func(ctx context.Context, _ *remotedialer.Session) error {
			logger.Info("tunnel established")
			backoff = reconnectMin
			return nil
		})
		if ctx.Err() != nil {
			_ = srv.Shutdown(context.Background())
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = reconnectMin // the connection lasted: not a persistent failure
		}
		logger.Warn("tunnel closed, retrying", "error", err, "in", backoff.String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > reconnectMax {
			backoff = reconnectMax
		}
	}
}

// newDockerProxy forwards to the Docker/Podman unix socket. No authentication:
// the socket is already root's and whoever gets here already passed the
// authenticated tunnel. FlushInterval -1 so that logs (follow) and events go
// out as they arrive.
func newDockerProxy(socket string, logger *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "docker"
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Warn("failed to forward to the daemon", "path", r.URL.Path, "error", err)
			http.Error(w, "agent: failed to forward to the daemon: "+err.Error(), http.StatusBadGateway)
		},
	}
	return rp
}

// apiProxy forwards to the API server adding the ServiceAccount token. The
// token is re-read from the file every minute: projected tokens rotate and the
// kubelet updates the file.
type apiProxy struct {
	rp        *httputil.ReverseProxy
	kubeTLS   *tls.Config // trust towards the API server, reused by the RBAC syncer
	tokenPath string
	mu        sync.Mutex
	token     string
	readAt    time.Time
	logger    *slog.Logger
	// lastErrLog throttles the forwarding error log: with the API server down,
	// every informer of the server retries and there would be one line per
	// request.
	lastErrLog time.Time
}

func newAPIProxy(apiHostPort, saDir string, logger *slog.Logger) (*apiProxy, error) {
	caPEM, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("failed to read the cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("the cluster CA is not a valid PEM")
	}
	target := &url.URL{Scheme: "https", Host: apiHostPort}
	kubeTLS := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	p := &apiProxy{logger: logger, tokenPath: saDir + "/token", kubeTLS: kubeTLS}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = &http.Transport{
		TLSClientConfig:       kubeTLS.Clone(),
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 0, // informer watches: no limit
	}
	// Negative FlushInterval: streaming responses (watch, logs) are flushed as
	// they arrive instead of waiting for the close.
	rp.FlushInterval = -1
	director := rp.Director
	rp.Director = func(req *http.Request) {
		director(req)
		req.Host = apiHostPort
		tok, err := p.currentToken()
		if err != nil {
			logger.Error("no ServiceAccount token", "error", err)
			return
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		p.mu.Lock()
		if time.Since(p.lastErrLog) > 10*time.Second {
			p.lastErrLog = time.Now()
			p.mu.Unlock()
			logger.Warn("failed to forward to the API server", "path", r.URL.Path, "error", err)
		} else {
			p.mu.Unlock()
		}
		http.Error(w, "agent: failed to forward to the API server: "+err.Error(), http.StatusBadGateway)
	}
	p.rp = rp
	return p, nil
}

func (p *apiProxy) currentToken() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Since(p.readAt) < tokenCacheTTL {
		return p.token, nil
	}
	b, err := os.ReadFile(p.tokenPath)
	if err != nil {
		return "", err
	}
	p.token = strings.TrimSpace(string(b))
	p.readAt = time.Now()
	return p.token, nil
}

func (p *apiProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.rp.ServeHTTP(w, r)
}

// serverTLSConfig is the TLS towards the panel. BAYPLAN_CA_CERT (base64 PEM,
// or plain PEM) is ADDED to the system CAs, not a replacement: a manifest
// carrying the server's self-signed CA must keep working if the public URL is
// later moved behind an ingress with a public certificate.
func serverTLSConfig(caCert string, insecure bool) (*tls.Config, error) {
	if insecure {
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // explicit operator choice
	}
	caCert = strings.TrimSpace(caCert)
	if caCert == "" {
		return nil, nil
	}
	pemData := []byte(caCert)
	if dec, err := base64.StdEncoding.DecodeString(caCert); err == nil {
		pemData = dec
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, errors.New("BAYPLAN_CA_CERT holds no PEM certificate")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
