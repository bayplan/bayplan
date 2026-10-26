// Package tlsserve gives the server its own HTTPS listener, so that a single
// container can be opened in a browser without a reverse proxy in front.
// Three certificate sources, in order of precedence:
//
//   - files (TLS_CERT_FILE/TLS_KEY_FILE): re-read when they change on disk, so
//     a renewal by certbot or cert-manager needs no restart;
//   - ACME (ACME_DOMAINS): Let's Encrypt with the TLS-ALPN-01 challenge, which
//     only needs the HTTPS port to be reachable as 443;
//   - self-signed (default): a private CA kept in the data volume issues a
//     certificate per requested name. Agents pin that CA (the manifest carries
//     it) instead of skipping verification.
//
// Behind an ingress or a proxy none of this is used: TLS_LISTEN_ADDR stays
// empty and the server keeps serving plain HTTP.
package tlsserve

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// Modes reported by Provider.Mode.
const (
	ModeFiles      = "files"
	ModeACME       = "acme"
	ModeSelfSigned = "self-signed"
)

// Options selects the certificate source.
type Options struct {
	CertFile, KeyFile string
	ACMEDomains       []string
	ACMEEmail         string
	ACMEDirectory     string // empty = Let's Encrypt production
	// DataDir keeps the self-signed CA and the ACME account/certificates.
	// Empty or not writable: they live in memory and change on every start.
	DataDir string
	// ExtraNames returns names (DNS or IP) every self-signed leaf also
	// covers: the host of the public URL. Without SNI (a URL with an IP) the
	// leaf is issued for the address the connection arrived at, which behind
	// a NAT (`docker run -p`, a Service) is the container's, not the one the
	// client dialed, and agents pinning the CA failed the hostname check.
	ExtraNames func() []string
}

// Provider hands out the server certificate for each TLS handshake.
type Provider struct {
	mode   string
	config *tls.Config
	caPEM  []byte
}

// New builds the provider for opts.
func New(opts Options, logger *slog.Logger) (*Provider, error) {
	switch {
	case opts.CertFile != "" || opts.KeyFile != "":
		if opts.CertFile == "" || opts.KeyFile == "" {
			return nil, errors.New("tls: TLS_CERT_FILE and TLS_KEY_FILE go together")
		}
		fc := &fileCert{certFile: opts.CertFile, keyFile: opts.KeyFile}
		if _, err := fc.get(nil); err != nil {
			return nil, err
		}
		return &Provider{mode: ModeFiles, config: baseConfig(fc.get)}, nil

	case len(opts.ACMEDomains) > 0:
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(opts.ACMEDomains...),
			Email:      opts.ACMEEmail,
		}
		if opts.ACMEDirectory != "" {
			m.Client = &acme.Client{DirectoryURL: opts.ACMEDirectory}
		}
		if dir := writableDir(opts.DataDir, "acme", logger); dir != "" {
			m.Cache = autocert.DirCache(dir)
		} else {
			// Without a cache every restart asks Let's Encrypt for a new
			// certificate, and its rate limit is 5 per week per name set.
			logger.Warn("tls: no writable data directory for the ACME cache; certificates will be requested again on every start")
		}
		cfg := m.TLSConfig()
		cfg.MinVersion = tls.VersionTLS12
		return &Provider{mode: ModeACME, config: cfg}, nil

	default:
		ca, err := loadOrCreateCA(writableDir(opts.DataDir, "tls", logger), logger)
		if err != nil {
			return nil, err
		}
		ca.extra = opts.ExtraNames
		return &Provider{mode: ModeSelfSigned, config: baseConfig(ca.get), caPEM: ca.certPEM}, nil
	}
}

// Mode is one of ModeFiles, ModeACME, ModeSelfSigned.
func (p *Provider) Mode() string { return p.mode }

// TLSConfig is the configuration for the HTTPS listener.
func (p *Provider) TLSConfig() *tls.Config { return p.config }

// CAPEM is the self-signed CA in PEM, which agents must trust. Nil in the
// other modes: there the certificate is expected to chain to a public or
// corporate CA the agent already knows (or the admin pastes it in Settings).
func (p *Provider) CAPEM() []byte { return p.caPEM }

func baseConfig(get func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: get,
		NextProtos:     []string{"h2", "http/1.1"},
	}
}

func writableDir(dataDir, sub string, logger *slog.Logger) string {
	if dataDir == "" {
		return ""
	}
	dir := filepath.Join(dataDir, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.Warn("tls: data directory not writable", "dir", dir, "error", err)
		return ""
	}
	return dir
}

// fileCert re-reads the pair when the certificate file changes. The check is
// a stat at most every 30 s, not per handshake.
type fileCert struct {
	certFile, keyFile string
	mu                sync.Mutex
	cert              *tls.Certificate
	modTime           time.Time
	checked           time.Time
}

func (f *fileCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cert != nil && time.Since(f.checked) < 30*time.Second {
		return f.cert, nil
	}
	f.checked = time.Now()
	st, err := os.Stat(f.certFile)
	if err != nil {
		if f.cert != nil {
			return f.cert, nil // keep serving the last good one during a renewal
		}
		return nil, fmt.Errorf("tls: %w", err)
	}
	if f.cert != nil && st.ModTime().Equal(f.modTime) {
		return f.cert, nil
	}
	c, err := tls.LoadX509KeyPair(f.certFile, f.keyFile)
	if err != nil {
		if f.cert != nil {
			return f.cert, nil
		}
		return nil, fmt.Errorf("tls: %w", err)
	}
	f.cert, f.modTime = &c, st.ModTime()
	return f.cert, nil
}

// selfCA issues leaf certificates on the fly for whatever name the client
// asked for (SNI) or, without SNI, for the IP it connected to. The URL users
// type is not known in advance (it can change in Settings), but the CA the
// agents pin does not change.
type selfCA struct {
	cert    *x509.Certificate
	key     crypto.Signer
	certPEM []byte

	extra func() []string

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// maxLeaves bounds the per-name cache: any client can ask for any SNI.
const maxLeaves = 128

func loadOrCreateCA(dir string, logger *slog.Logger) (*selfCA, error) {
	if dir != "" {
		certPEM, err1 := os.ReadFile(filepath.Join(dir, "ca.crt"))
		keyPEM, err2 := os.ReadFile(filepath.Join(dir, "ca.key"))
		if err1 == nil && err2 == nil {
			ca, err := parseCA(certPEM, keyPEM)
			if err != nil {
				return nil, fmt.Errorf("tls: %s: %w (delete ca.crt and ca.key to generate a new CA; agents will need their manifest reapplied)", dir, err)
			}
			return ca, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Bayplan self-signed CA", Organization: []string{"Bayplan"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "ca.key"), keyPEM, 0o600); err != nil {
			return nil, fmt.Errorf("tls: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ca.crt"), certPEM, 0o644); err != nil {
			return nil, fmt.Errorf("tls: %w", err)
		}
		logger.Info("tls: generated a self-signed CA", "dir", dir)
	} else {
		logger.Warn("tls: self-signed CA kept in memory only; agents will stop trusting the server after a restart")
	}
	return parseCA(certPEM, keyPEM)
}

func parseCA(certPEM, keyPEM []byte) (*selfCA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("invalid PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &selfCA{cert: cert, key: key, certPEM: certPEM, leaves: map[string]*tls.Certificate{}}, nil
}

func (ca *selfCA) get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := ""
	if hello != nil {
		name = hello.ServerName
		if name == "" && hello.Conn != nil {
			if addr, ok := hello.Conn.LocalAddr().(*net.TCPAddr); ok {
				name = addr.IP.String()
			}
		}
	}
	if name == "" {
		name = "localhost"
	}
	var extra []string
	if ca.extra != nil {
		extra = ca.extra()
	}
	// The extra names are part of the key: changing the public URL must not
	// keep serving a cached leaf without the new host.
	key := strings.Join(append([]string{name}, extra...), ",")
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if c, ok := ca.leaves[key]; ok && time.Until(c.Leaf.NotAfter) > 30*24*time.Hour {
		return c, nil
	}
	c, err := ca.issue(name, time.Now(), extra...)
	if err != nil {
		return nil, err
	}
	if len(ca.leaves) >= maxLeaves {
		ca.leaves = map[string]*tls.Certificate{}
	}
	ca.leaves[key] = c
	return c, nil
}

// issue creates a leaf for name and extra (DNS names or IPs) plus localhost.
func (ca *selfCA) issue(name string, now time.Time, extra ...string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: name, Organization: []string{"Bayplan"}},
		NotBefore:    now.Add(-time.Hour),
		// 397 days: the longest validity browsers accept without complaint.
		NotAfter:    now.AddDate(0, 0, 397),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	seen := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	for _, n := range append([]string{name}, extra...) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}
