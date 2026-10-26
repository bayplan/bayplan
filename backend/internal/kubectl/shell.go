// Package kubectl offers a per-cluster kubectl web shell: the user types
// kubectl commands in the browser and the server runs them with the real
// binary against the cluster, with the user's impersonated identity.
//
// It is not a system shell: every line is parsed, only `kubectl` (or `k`) and
// a few builtins are accepted, and the subcommands and flags that would touch
// the SERVER instead of the cluster are rejected (reading local files with -f,
// opening ports with port-forward, switching credentials with --kubeconfig…).
// kubectl talks to an ephemeral local HTTP proxy that adds the credential and
// the impersonation with the same rest.Config the other actions use, so it
// works the same with the direct connector, the agent and cloud, and the
// binary never sees the cluster token.
package kubectl

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	maxSessions = 20
	// idleTimeout closes an idle shell: each one keeps a proxy and a temporary
	// directory.
	idleTimeout = 30 * time.Minute
)

var (
	ErrUnavailable = errors.New("kubectl is not available on the server")
	ErrTooMany     = errors.New("too many kubectl shells open, try again later")
	ErrNotAllowed  = errors.New("command not allowed in the web shell")
	errNoCommand   = errors.New("")
	blockedSubcmds = map[string]string{
		"proxy":        "would open a port on the server",
		"port-forward": "would open a port on the server",
		"cp":           "would read or write files on the server",
		"edit":         "needs an interactive editor; use the panel's YAML editor",
		"config":       "the kubeconfig is set by the panel",
		"plugin":       "plugins run on the server",
		"completion":   "does not apply in the web shell",
		"kustomize":    "would read a directory on the server",
		"alpha":        "is not supported in the web shell",
	}
	// allowedSubcmds is what the shell runs. Anything not here is rejected
	// even if it is not in blockedSubcmds (which exists only to give a
	// readable reason for the frequent cases).
	allowedSubcmds = map[string]bool{
		"get": true, "describe": true, "logs": true, "events": true, "top": true, "explain": true,
		"api-resources": true, "api-versions": true, "version": true, "cluster-info": true, "auth": true,
		"apply": true, "create": true, "delete": true, "replace": true, "patch": true, "diff": true,
		"label": true, "annotate": true, "set": true, "expose": true, "run": true, "wait": true,
		"rollout": true, "scale": true, "autoscale": true, "exec": true, "attach": true, "debug": true,
		"cordon": true, "uncordon": true, "drain": true, "taint": true, "certificate": true,
	}
	// blockedFlags would change the target or the identity, or write to disk.
	blockedFlags = []string{
		"--kubeconfig", "--server", "-s", "--token", "--as", "--as-group", "--as-uid",
		"--insecure-skip-tls-verify", "--certificate-authority", "--client-certificate", "--client-key",
		"--context", "--cluster", "--user", "--username", "--password", "--cache-dir",
		"--tls-server-name", "--profile", "--profile-output", "--disable-compression", "--output-directory",
	}
	// fileFlags read local files: only stdin (-) is accepted. No URLs either:
	// kubectl would download them FROM THE SERVER (SSRF towards the internal
	// network or the cloud metadata); the user pastes the YAML via stdin.
	fileFlags = []string{"-f", "--filename", "-k", "--kustomize", "--from-file", "--from-env-file", "--patch-file", "--template", "--cert", "--key", "--ca-cert", "--token-file", "--custom"}
	// shortFlagsWithValue are the short flags that take a value. In a short
	// group ("-Rf x", "-Af/etc/passwd", "-itc sh") the first of these that
	// appears consumes the rest of the argument (or the next one) as its
	// value; the previous ones are booleans. That is how pflag parses it.
	// -v is klog's verbosity (global) and -e is `set env --env`.
	shortFlagsWithValue = "fkonlLcspve"
)

// Runner knows the binary and creates sessions.
type Runner struct {
	path   string
	active atomic.Int64
	// cert is the local proxy's TLS certificate (self-signed for 127.0.0.1,
	// one per process): client-go does NOT send bearer tokens to a cleartext
	// http:// server, so the proxy has to be https even on loopback. Each
	// session's kubeconfig carries this certificate as CA.
	cert    tls.Certificate
	certPEM []byte
}

// New locates kubectl. An empty path looks for "kubectl" in the server's
// PATH. If it does not exist, Available() is false and the route responds
// 404: the shell is optional (the official image includes it).
func New(path string) *Runner {
	if path == "" {
		path = "kubectl"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return &Runner{}
	}
	cert, certPEM, err := selfSignedLoopbackCert()
	if err != nil {
		return &Runner{}
	}
	cleanupStaleDirs()
	return &Runner{path: resolved, cert: cert, certPEM: certPEM}
}

// cleanupStaleDirs deletes the session directories a server restart left
// behind (each session cleans up on close, but a SIGKILL does not go through
// there). They only hold a kubeconfig and the discovery cache.
func cleanupStaleDirs() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "bayplan-shell-*"))
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}

// selfSignedLoopbackCert generates an ECDSA certificate for 127.0.0.1 valid
// for as long as the process lives (and a bit more, for clock skew).
func selfSignedLoopbackCert() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "bayplan-kubectl-proxy"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	return cert, certPEM, err
}

func (r *Runner) Available() bool { return r.path != "" }

// Active returns the open shells (metric).
func (r *Runner) Active() int64 { return r.active.Load() }

// Session is an open shell: its proxy, its directory and the running process.
type Session struct {
	runner    *Runner
	dir       string
	proxy     *http.Server
	listener  net.Listener
	token     string
	namespace string
	protected string
	env       []string
	cancel    context.CancelFunc
	ctx       context.Context
}

// Open prepares the shell: local proxy towards the cluster with the
// impersonated rest.Config, a kubeconfig pointing at it and a PATH with only
// kubectl. protected is the cluster's protected namespace ("" if none): the
// proxy refuses every request into it, whatever kubectl was asked.
func (r *Runner) Open(ctx context.Context, cfg *rest.Config, namespace, protected string) (*Session, error) {
	if !r.Available() {
		return nil, ErrUnavailable
	}
	if r.active.Load() >= maxSessions {
		return nil, ErrTooMany
	}
	dir, err := os.MkdirTemp("", "bayplan-shell-")
	if err != nil {
		return nil, err
	}
	s := &Session{runner: r, dir: dir, namespace: namespace, protected: protected}
	s.ctx, s.cancel = context.WithCancel(ctx)
	if err := s.startProxy(cfg); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.writeKubeconfig(); err != nil {
		s.Close()
		return nil, err
	}
	// PATH with a single binary: kubectl looks for kubectl-* plugins in the
	// PATH and here it must find nothing but itself.
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		s.Close()
		return nil, err
	}
	if err := os.Symlink(r.path, filepath.Join(bin, "kubectl")); err != nil {
		s.Close()
		return nil, err
	}
	s.env = []string{
		"PATH=" + bin,
		"HOME=" + dir,
		"KUBECONFIG=" + filepath.Join(dir, "kubeconfig"),
		"KUBECACHEDIR=" + filepath.Join(dir, "cache"),
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
		"KUBE_EDITOR=false",
		"EDITOR=false",
		"NO_COLOR=", // kubectl does not colorize by default; the decision is left to the absent plugins
	}
	r.active.Add(1)
	return s, nil
}

// startProxy listens on 127.0.0.1 on a free port and forwards to the API
// server with the rest.Config transport (TLS, token, impersonation, agent
// tunnel, cloud token: client-go resolves all of it). It requires the session
// token in Authorization so that another local process cannot use the proxy,
// and strips it before forwarding.
func (s *Session) startProxy(cfg *rest.Config) error {
	target, err := url.Parse(cfg.Host)
	if err != nil {
		return fmt.Errorf("kubectl: invalid host %q: %w", cfg.Host, err)
	}
	// HTTP/1.1 only towards the API server: `exec`/`attach`/`port-forward` are
	// Upgrade (SPDY/WebSocket) and there is no Upgrade over HTTP/2; with h2
	// negotiated the transport rejects the request and kubectl gets an empty
	// 502 ("Error from server:").
	cfg = rest.CopyConfig(cfg)
	cfg.NextProtos = []string{"http/1.1"}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return fmt.Errorf("kubectl: failed to create transport: %w", err)
	}
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return err
	}
	s.token = hex.EncodeToString(tok)

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Authorization")
			// client-go keeps an Impersonate-* header already present on the
			// request; the validator blocks --as, but the proxy is the last
			// line and must not depend on the line parser.
			for k := range pr.Out.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(k), "Impersonate-") {
					pr.Out.Header.Del(k)
				}
			}
		},
		Transport:     transport,
		FlushInterval: -1, // watch/logs -f: every chunk goes out immediately
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.listener = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{s.runner.cert}, MinVersion: tls.VersionTLS12})
	expected := "Bearer " + s.token
	s.proxy = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != expected {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if protectedPathBlocked(r.Method, r.URL, s.protected) {
				http.Error(w, "namespace "+s.protected+" is the panel's own", http.StatusForbidden)
				return
			}
			rp.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = s.proxy.Serve(s.listener) }()
	return nil
}

// protectedPathBlocked is the proxy's half of the protected-namespace guard:
// the validator only sees the command line, not what kubectl derives from
// stdin (`get -f -` with a manifest whose namespace is the protected one) or
// from label selectors (`delete ns -l ...`), but every one of those ends up
// as a request through here. Anything under /namespaces/<protected>/ is
// refused, and so is any write to the Namespace object itself; reading the
// Namespace object is harmless and `kubectl get ns` needs it. A path that is
// not clean or carries escapes is refused too: the API server would resolve
// it to something this check did not look at.
func protectedPathBlocked(method string, u *url.URL, protected string) bool {
	if protected == "" {
		return false
	}
	p := u.Path
	if clean := path.Clean(p); u.RawPath != "" || (clean != p && clean+"/" != p) {
		return true
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	var rest []string
	switch {
	case len(segs) >= 2 && segs[0] == "api":
		rest = segs[2:]
	case len(segs) >= 3 && segs[0] == "apis":
		rest = segs[3:]
	default:
		return false
	}
	if len(rest) > 0 && rest[0] == "watch" {
		rest = rest[1:]
	}
	if len(rest) < 2 || rest[0] != "namespaces" || rest[1] != protected {
		return false
	}
	if len(rest) == 2 && (method == http.MethodGet || method == http.MethodHead) {
		return false
	}
	return true
}

func (s *Session) writeKubeconfig() error {
	kc := clientcmdapi.NewConfig()
	kc.Clusters["panel"] = &clientcmdapi.Cluster{Server: "https://" + s.listener.Addr().String(), CertificateAuthorityData: s.runner.certPEM}
	kc.AuthInfos["panel"] = &clientcmdapi.AuthInfo{Token: s.token}
	kc.Contexts["panel"] = &clientcmdapi.Context{Cluster: "panel", AuthInfo: "panel", Namespace: s.namespace}
	kc.CurrentContext = "panel"
	return clientcmd.WriteToFile(*kc, filepath.Join(s.dir, "kubeconfig"))
}

// Close shuts down the proxy, kills the running process and deletes the directory.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.proxy != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.proxy.Shutdown(ctx)
		cancel()
	} else if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
	if s.runner != nil && s.env != nil {
		s.runner.active.Add(-1)
		s.env = nil
	}
}

// Validate parses the line and returns the arguments to run (without the
// leading "kubectl") or a readable error to show in the shell. It is pure and
// tested (shell_test.go).
func Validate(line string) ([]string, error) { return ValidateProtected(line, "", "") }

// ValidateProtected is Validate plus the block on the local cluster's
// protected namespace (the panel's own): neither -n/--namespace to that
// namespace, nor -A/--all-namespaces (which would include it), nor --raw
// (arbitrary paths), nor a mutating verb on the Namespace object itself, nor a
// session whose context namespace (contextNS, the kubeconfig default) is that
// one. Reading Secrets there is exactly what a panel admin must not be able to
// do. The session's local proxy enforces the same thing on the request paths
// (guardProtectedPath), which also covers what the validator cannot see, such
// as the manifests piped to `get -f -`.
func ValidateProtected(line, protected, contextNS string) ([]string, error) {
	words, err := splitWords(line)
	if err != nil {
		return nil, err
	}
	if len(words) == 0 {
		return nil, errNoCommand
	}
	switch words[0] {
	case "kubectl", "k":
	default:
		return nil, fmt.Errorf("%w: only kubectl is accepted (type help)", ErrNotAllowed)
	}
	args := words[1:]
	// The subcommand has to be the FIRST word after kubectl: guessing it by
	// skipping flags is fooled by a flag's value ("-n default cp …" would
	// pass "default" off as the subcommand). And it is an allowlist, not a
	// denylist: a new kubectl subcommand does not get in until someone
	// reviews it.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return nil, fmt.Errorf("%w: write the subcommand right after kubectl (kubectl get pods -n x, not kubectl -n x get pods)", ErrNotAllowed)
	}
	subcmd := args[0]
	if reason, blocked := blockedSubcmds[subcmd]; blocked {
		return nil, fmt.Errorf("%w: kubectl %s %s", ErrNotAllowed, subcmd, reason)
	}
	if !allowedSubcmds[subcmd] {
		return nil, fmt.Errorf("%w: subcommand %q is not in the allowed list (type help)", ErrNotAllowed, subcmd)
	}
	if protected != "" && contextNS == protected {
		return nil, fmt.Errorf("%w: namespace %s is the panel's own", ErrNotAllowed, protected)
	}
	var positionals []string
	// awaiting: the previous token was a flag that takes a value and did not
	// carry one attached, so pflag consumes THIS token as its value whatever
	// it is, "--" included ("-L -- -n x": "--" is the value of -L and -n is
	// still parsed as a flag). Every token is still checked as a flag too: a
	// misjudged value only costs a false rejection, never a bypass.
	awaiting := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		wasValue := awaiting
		awaiting = false
		if a == "--" && !wasValue {
			break // what follows is the remote command of exec/run
		}
		if !wasValue && (!strings.HasPrefix(a, "-") || a == "-") {
			positionals = append(positionals, a)
			continue
		}
		flags := parseFlag(a, subcmd)
		for _, f := range flags {
			next := ""
			if !f.hasValue && i+1 < len(args) {
				next = args[i+1]
			}
			if err := checkFlag(f, next, subcmd, protected); err != nil {
				return nil, err
			}
		}
		if n := len(flags); n > 0 && !wasValue {
			last := flags[n-1]
			awaiting = !last.hasValue && takesValue(last.name, subcmd)
		}
	}
	if protected != "" && mutatingSubcmds[subcmd] && targetsNamespaceObject(positionals, protected) {
		return nil, fmt.Errorf("%w: namespace %s is the panel's own", ErrNotAllowed, protected)
	}
	return args, nil
}

// mutatingSubcmds are the verbs that would change the Namespace object
// itself when given "ns <protected>" or "ns/<protected>" (deleting it takes
// the panel, its Secrets and its database with it).
var mutatingSubcmds = map[string]bool{
	"delete": true, "label": true, "annotate": true, "patch": true, "replace": true, "apply": true,
	"create": true, "set": true,
}

func isNamespaceType(t string) bool {
	switch strings.ToLower(t) {
	case "ns", "namespace", "namespaces", "namespace.v1", "namespaces.v1":
		return true
	}
	return false
}

// targetsNamespaceObject: "ns/<p>" (or a comma list containing it), or a
// type list that includes namespaces followed by <p> as a name.
func targetsNamespaceObject(positionals []string, protected string) bool {
	typeIsNS := false
	for i, p := range positionals {
		for _, part := range strings.Split(p, ",") {
			if t, name, ok := strings.Cut(part, "/"); ok && isNamespaceType(t) && name == protected {
				return true
			}
			if i == 0 && isNamespaceType(part) {
				typeIsNS = true
			}
		}
		if i > 0 && typeIsNS && p == protected {
			return true
		}
	}
	return false
}

type flagTok struct {
	name, value string
	hasValue    bool
}

// shortTakesValue: in `logs`, -f is --follow and -p is --previous (booleans);
// everywhere else they take a value (--filename, --patch / --pod).
func shortTakesValue(c byte, subcmd string) bool {
	if subcmd == "logs" && (c == 'f' || c == 'p') {
		return false
	}
	return strings.IndexByte(shortFlagsWithValue, c) >= 0
}

// takesValue says whether a flag WITHOUT an attached value consumes the next
// argument. For long flags the unknown ones are assumed to take a value: the
// only effect is that a following "--" is not taken as the end of the flags,
// so more is checked, never less.
func takesValue(name, subcmd string) bool {
	if strings.HasPrefix(name, "--") {
		return !booleanLongFlags[strings.TrimPrefix(name, "--")]
	}
	if len(name) == 2 {
		return shortTakesValue(name[1], subcmd)
	}
	return false
}

// booleanLongFlags are long flags that never consume the next argument (bool,
// or with a default when given without a value, like --dry-run).
var booleanLongFlags = map[string]bool{
	"stdin": true, "tty": true, "quiet": true, "all-namespaces": true, "all": true, "force": true,
	"overwrite": true, "recursive": true, "watch": true, "watch-only": true, "show-labels": true,
	"no-headers": true, "follow": true, "previous": true, "timestamps": true, "prefix": true,
	"all-containers": true, "ignore-daemonsets": true, "delete-emptydir-data": true, "rm": true,
	"attach": true, "leave-stdin-open": true, "dry-run": true, "validate": true, "server-side": true,
	"record": true, "local": true, "ignore-not-found": true, "now": true, "cascade": true,
	"privileged": true, "wait": true, "show-kind": true, "show-managed-fields": true, "help": true,
	"force-conflicts": true, "prune": true, "keep-annotations": true, "list": true, "insecure-skip-tls-verify": true,
	"disable-compression": true, "warnings-as-errors": true, "match-server-version": true, "containers": true,
}

// parseFlag splits an argument into the flags pflag sees in it. "--flag=v" is
// one flag. A short group ("-it", "-Rf v", "-Af/etc/passwd", "-A=true") is
// walked letter by letter as pflag does: each letter is a flag; "=" right
// after a letter gives it the rest as value; the FIRST letter that takes a
// value consumes the rest of the argument (or, if nothing is left, the next
// argument). Without this, "-A=true" or "-RA" went unseen.
func parseFlag(a, subcmd string) []flagTok {
	if !strings.HasPrefix(a, "-") || a == "-" {
		return nil
	}
	if strings.HasPrefix(a, "--") {
		name, value, has := strings.Cut(a, "=")
		return []flagTok{{name, value, has}}
	}
	var out []flagTok
	for i := 1; i < len(a); i++ {
		c := a[i]
		name := "-" + string(c)
		if i+1 < len(a) && a[i+1] == '=' {
			return append(out, flagTok{name, a[i+2:], true})
		}
		if shortTakesValue(c, subcmd) {
			if i+1 < len(a) {
				return append(out, flagTok{name, a[i+1:], true})
			}
			return append(out, flagTok{name, "", false})
		}
		out = append(out, flagTok{name, "", false})
	}
	return out
}

// checkFlag applies the rules to one flag; next is the following argument,
// which is the flag's value when it carried none attached.
func checkFlag(f flagTok, next, subcmd, protected string) error {
	value := f.value
	if !f.hasValue {
		value = next
	}
	if f.name == "--raw" {
		return fmt.Errorf("%w: --raw sends arbitrary paths to the API server; use the resource commands", ErrNotAllowed)
	}
	if protected != "" {
		if f.name == "-A" || f.name == "--all-namespaces" {
			return fmt.Errorf("%w: on this cluster --all-namespaces would include the panel's own namespace (%s); specify the namespace", ErrNotAllowed, protected)
		}
		if (f.name == "-n" || f.name == "--namespace") && value == protected {
			return fmt.Errorf("%w: namespace %s is the panel's own", ErrNotAllowed, protected)
		}
	}
	for _, b := range blockedFlags {
		if f.name == b {
			return fmt.Errorf("%w: flag %s is set by the panel", ErrNotAllowed, f.name)
		}
	}
	if f.name == "-o" || f.name == "--output" {
		// Every "*-file" format (go-template-file, jsonpath-file,
		// custom-columns-file) reads a template from the server.
		if format, _, _ := strings.Cut(value, "="); strings.HasSuffix(format, "-file") {
			return fmt.Errorf("%w: %s reads a file from the server", ErrNotAllowed, value)
		}
	}
	for _, ff := range fileFlags {
		if f.name != ff {
			continue
		}
		// In `logs`, -f is --follow, not --filename.
		if ff == "-f" && subcmd == "logs" {
			continue
		}
		if !f.hasValue && next == "" {
			continue // kubectl itself fails: the flag needs an argument
		}
		if !fileArgAllowed(value) {
			return fmt.Errorf("%w: %s only accepts '-' (stdin): the server does not have your files and must not download URLs; paste the YAML and finish with Ctrl+D", ErrNotAllowed, ff)
		}
	}
	return nil
}

// fileArgAllowed: "-" (stdin) or "key=-" in --from-file. Nothing else.
func fileArgAllowed(v string) bool {
	if _, rest, ok := strings.Cut(v, "="); ok {
		v = rest
	}
	return v == "-"
}

// splitWords splits the line like a simple POSIX shell: spaces, single and
// double quotes, backslash. No expansions or operators: there is no shell
// behind it.
func splitWords(line string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	quote := rune(0)
	escaped := false
	for _, c := range line {
		switch {
		case escaped:
			cur.WriteRune(c)
			escaped = false
			inWord = true
		case c == '\\' && quote != '\'':
			escaped = true
			inWord = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '|' || c == ';' || c == '&' || c == '>' || c == '<' || c == '`' || c == '$':
			return nil, fmt.Errorf("%w: there is no shell behind this, pipes, redirections and variables are not accepted", ErrNotAllowed)
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("unclosed quotes")
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// Command is a kubectl running inside a PTY.
type Command struct {
	cmd *exec.Cmd
	pty *os.File
}

// Run launches kubectl with the already validated arguments inside a PTY of
// the given size. The PTY is what makes `exec -it`, `logs -f` and colors work
// as in a real terminal.
func (s *Session) Run(args []string, cols, rows uint16) (*Command, error) {
	cmd := exec.CommandContext(s.ctx, s.runner.path, args...)
	cmd.Env = s.env
	cmd.Dir = s.dir
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("could not launch kubectl: %w", err)
	}
	return &Command{cmd: cmd, pty: f}, nil
}

// PTY returns the master end: what is read is the process output and what is
// written is its keyboard.
func (c *Command) PTY() *os.File { return c.pty }

// Resize changes the size of the process terminal.
func (c *Command) Resize(cols, rows uint16) {
	_ = pty.Setsize(c.pty, &pty.Winsize{Cols: cols, Rows: rows})
}

// Wait waits for the process and returns its exit code (-1 if a signal killed it).
func (c *Command) Wait() int {
	err := c.cmd.Wait()
	_ = c.pty.Close()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// Kill kills the process (WebSocket closed).
func (c *Command) Kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// IdleTimeout exposes the maximum idle time.
func IdleTimeout() time.Duration { return idleTimeout }

// IsNoCommand reports whether the line was empty.
func IsNoCommand(err error) bool { return errors.Is(err, errNoCommand) }
