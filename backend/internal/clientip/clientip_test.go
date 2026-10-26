package clientip

import (
	"net/http/httptest"
	"testing"
)

func TestFromRequest(t *testing.T) {
	cases := []struct {
		name    string
		trusted []string
		remote  string
		xff     string
		want    string
	}{
		{"no proxies configured ignores XFF", nil, "10.0.0.5:1234", "1.2.3.4", "10.0.0.5"},
		{"untrusted remote ignores XFF", []string{"10.0.0.0/8"}, "192.168.1.9:80", "1.2.3.4", "192.168.1.9"},
		{"trusted remote takes XFF", []string{"10.0.0.0/8"}, "10.0.0.5:80", "1.2.3.4", "1.2.3.4"},
		{"chain: skips trusted proxies from the right", []string{"10.0.0.0/8"}, "10.0.0.5:80", "9.9.9.9, 1.2.3.4, 10.0.0.7", "1.2.3.4"},
		{"corrupt XFF falls back to remote", []string{"10.0.0.0/8"}, "10.0.0.5:80", "not-an-ip", "10.0.0.5"},
		{"bare IP as implicit CIDR", []string{"10.0.0.5"}, "10.0.0.5:80", "1.2.3.4", "1.2.3.4"},
		{"ipv6 remote", []string{"::1"}, "[::1]:443", "2001:db8::1", "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := SetTrustedProxies(tc.trusted); err != nil {
				t.Fatalf("SetTrustedProxies: %v", err)
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := FromRequest(r); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	_ = SetTrustedProxies(nil)
}

func TestSetTrustedProxiesInvalid(t *testing.T) {
	if err := SetTrustedProxies([]string{"300.1.1.1"}); err == nil {
		t.Fatal("expected an error with an invalid IP")
	}
	if err := SetTrustedProxies([]string{"10.0.0.0/99"}); err == nil {
		t.Fatal("expected an error with an invalid CIDR")
	}
}
