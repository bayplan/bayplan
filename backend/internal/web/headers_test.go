package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, isHijacker := w.(http.Hijacker); !isHijacker {
			t.Error("the ResponseWriter must keep its Hijacker for WebSocket upgrades")
		}
	})
	for _, hsts := range []bool{false, true} {
		srv := httptest.NewServer(SecurityHeaders(ok, hsts))
		resp, err := http.Get(srv.URL)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for k, v := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "strict-origin-when-cross-origin",
			"X-Frame-Options":        "DENY",
		} {
			if got := resp.Header.Get(k); got != v {
				t.Errorf("%s = %q, expected %q", k, got, v)
			}
		}
		if got := resp.Header.Get("Strict-Transport-Security"); (got != "") != hsts {
			t.Errorf("hsts=%v: Strict-Transport-Security = %q", hsts, got)
		}
		if resp.Header.Get("Content-Security-Policy") != "" {
			t.Error("no CSP on purpose")
		}
	}
}
