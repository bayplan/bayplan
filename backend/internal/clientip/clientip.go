// Package clientip resolves the source IP of an HTTP request for auditing,
// rate limiting and sessions.
//
// It lives in its own package because both middleware and session need it,
// and middleware already imports session: putting it in either of the two
// would force duplicating it (which is what there was: two identical copies
// that both ignored X-Forwarded-For).
package clientip

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
)

var (
	mu      sync.RWMutex
	trusted []*net.IPNet
)

// SetTrustedProxies sets the networks from which X-Forwarded-For is accepted.
// With an empty list (the default) the header is always ignored: any client
// can forge it, so it is only credible when set by a reverse proxy that we
// control and that sanitizes it. The documented production deployment ALWAYS
// has a TLS proxy in front, so without this the audit trail and the login
// attempt limiter would only ever see the proxy's IP.
func SetTrustedProxies(cidrs []string) error {
	var nets []*net.IPNet
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// A bare IP is accepted as a shorthand for /32 (or /128).
		if !strings.Contains(raw, "/") {
			ip := net.ParseIP(raw)
			if ip == nil {
				return fmt.Errorf("clientip: invalid trusted proxy %q", raw)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			raw = fmt.Sprintf("%s/%d", ip, bits)
		}
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			return fmt.Errorf("clientip: invalid trusted proxy %q: %w", raw, err)
		}
		nets = append(nets, n)
	}
	mu.Lock()
	trusted = nets
	mu.Unlock()
	return nil
}

// FromRequest returns the client's IP. If RemoteAddr belongs to a trusted
// proxy and X-Forwarded-For is present, the RIGHTMOST IP that is not a trusted
// proxy is taken: it is the last one appended by one of our proxies, and
// anything to its left could have been written by the client.
func FromRequest(r *http.Request) string {
	remote := hostOnly(r.RemoteAddr)
	remoteIP := net.ParseIP(remote)
	if remoteIP == nil || !isTrusted(remoteIP) {
		if r.Header.Get("X-Forwarded-For") != "" {
			warnUntrustedProxy(remote)
		}
		return remote
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remote
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		ip := net.ParseIP(candidate)
		if ip == nil {
			return remote
		}
		if !isTrusted(ip) {
			return ip.String()
		}
	}
	// Every IP in the chain is one of our proxies: the leftmost one is the
	// best approximation available.
	if ip := net.ParseIP(strings.TrimSpace(parts[0])); ip != nil {
		return ip.String()
	}
	return remote
}

func isTrusted(ip net.IP) bool {
	mu.RLock()
	defer mu.RUnlock()
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

var untrustedOnce sync.Once

// warnUntrustedProxy says once that a proxy is in front but not trusted:
// every client then shares the proxy's IP in the audit log and in the per-IP
// login limit, so one client throttles everybody's login (only browsers with
// a device cookie get through). It is the usual symptom of a chart installed
// without trustedProxies.
func warnUntrustedProxy(remote string) {
	untrustedOnce.Do(func() {
		slog.Warn("requests carry X-Forwarded-For from an address not in TRUSTED_PROXIES: every client shares that address in the audit log and the per-IP login limit; set TRUSTED_PROXIES to the proxy's network",
			"proxy", remote)
	})
}
