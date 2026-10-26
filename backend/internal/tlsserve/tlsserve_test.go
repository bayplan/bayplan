package tlsserve

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// The point of the self-signed mode: a client that only trusts the CA (what
// the agent does) verifies the certificate for whatever name it used.
func TestSelfSignedVerifiesAgainstCA(t *testing.T) {
	dir := t.TempDir()
	p, err := New(Options{DataDir: dir}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode() != ModeSelfSigned || len(p.CAPEM()) == 0 {
		t.Fatalf("mode = %s, ca = %d bytes", p.Mode(), len(p.CAPEM()))
	}
	// A plain tls.Listen: httptest's StartTLS adds its own certificate, which
	// the server prefers over GetCertificate when the client sends no SNI.
	ln, err := tls.Listen("tcp", "127.0.0.1:0", p.TLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.CAPEM())
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	for _, serverName := range []string{"bayplan.example.internal", ""} {
		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: serverName},
		}}
		target := "https://127.0.0.1:" + port
		if serverName == "" {
			client.Transport.(*http.Transport).TLSClientConfig.ServerName = "127.0.0.1"
		}
		resp, err := client.Get(target)
		if err != nil {
			t.Fatalf("server name %q: %v", serverName, err)
		}
		resp.Body.Close()
	}

	// The CA survives a restart: agents keep trusting the server.
	p2, err := New(Options{DataDir: dir}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if string(p2.CAPEM()) != string(p.CAPEM()) {
		t.Fatal("the CA changed after a restart")
	}
	if st, err := os.Stat(filepath.Join(dir, "tls", "ca.key")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("ca.key must exist with 0600: %v %v", st, err)
	}
}

func TestFilesRequireBoth(t *testing.T) {
	if _, err := New(Options{CertFile: "/x.crt"}, discard); err == nil {
		t.Fatal("a cert without a key must be rejected")
	}
}

func TestFilesReloadKeepsLastGood(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrCreateCA("", discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePair(ca, "a.example", filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")); err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key")}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode() != ModeFiles || p.CAPEM() != nil {
		t.Fatalf("mode = %s", p.Mode())
	}
	c, err := p.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || c == nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	// A renewal in progress (file missing) must not break the handshakes.
	_ = os.Remove(filepath.Join(dir, "tls.crt"))
	fc := &fileCert{certFile: filepath.Join(dir, "tls.crt"), keyFile: filepath.Join(dir, "tls.key"), cert: c}
	if got, err := fc.get(nil); err != nil || got != c {
		t.Fatalf("expected the last good certificate, got %v %v", got, err)
	}
}

func writePair(ca *selfCA, name, certFile, keyFile string) error {
	c, err := ca.issue(name, timeNow())
	if err != nil {
		return err
	}
	certPEM, keyPEM, err := encodePair(c)
	if err != nil {
		return err
	}
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(keyFile, keyPEM, 0o600)
}

func timeNow() time.Time { return time.Now() }

func encodePair(c *tls.Certificate) (certPEM, keyPEM []byte, err error) {
	keyDER, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// Behind a NAT (`docker run -p`) a client that dials the public IP sends no
// SNI and the server only sees its own address: the leaf must still cover the
// public URL's host, or agents pinning the CA fail the hostname check.
func TestSelfSignedCoversPublicHostWithoutSNI(t *testing.T) {
	public := "203.0.113.7"
	for _, withExtra := range []bool{false, true} {
		opts := Options{DataDir: t.TempDir()}
		if withExtra {
			opts.ExtraNames = func() []string { return []string{public} }
		}
		p, err := New(opts, discard)
		if err != nil {
			t.Fatal(err)
		}
		ln, err := tls.Listen("tcp", "127.0.0.1:0", p.TLSConfig())
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })}
		go func() { _ = srv.Serve(ln) }()

		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(p.CAPEM())
		// An IP as ServerName is verified but never sent as SNI.
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: public}}}
		resp, err := client.Get("https://" + ln.Addr().String())
		if resp != nil {
			resp.Body.Close()
		}
		srv.Close()
		if withExtra && err != nil {
			t.Fatalf("with the public host as extra name: %v", err)
		}
		if !withExtra && err == nil {
			t.Fatal("without extra names the leaf should not cover the public IP")
		}
	}
}
