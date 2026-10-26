package main

import (
	"encoding/base64"
	"io"
	"log/slog"
	"testing"

	"github.com/bayplan/bayplan/internal/tlsserve"
)

func TestServerTLSConfig(t *testing.T) {
	if cfg, err := serverTLSConfig("", false); err != nil || cfg != nil {
		t.Fatalf("without a CA the defaults (system CAs) must be used, got %v %v", cfg, err)
	}
	if cfg, _ := serverTLSConfig("ignored", true); cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatal("insecure must win")
	}
	p, err := tlsserve.New(tlsserve.Options{DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]string{
		"base64 (what the manifest carries)": base64.StdEncoding.EncodeToString(p.CAPEM()),
		"plain PEM":                          string(p.CAPEM()),
	} {
		cfg, err := serverTLSConfig(in, false)
		if err != nil || cfg == nil || cfg.RootCAs == nil || cfg.InsecureSkipVerify {
			t.Fatalf("%s: %v %v", name, cfg, err)
		}
	}
	if _, err := serverTLSConfig("bm90IGEgY2VydA==", false); err == nil {
		t.Fatal("garbage must be rejected at startup, not at the first handshake")
	}
}
