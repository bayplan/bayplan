package web

import "net/http"

// SecurityHeaders adds the response headers every page and API response of
// the panel carries. Only headers are set (the ResponseWriter is not
// wrapped), so WebSocket upgrades keep their Hijacker.
//
// There is deliberately no Content-Security-Policy: the UI extensions embed
// arbitrary admin-chosen tools in iframes, and a frame-src that does not take
// them into account would leave them blank without warning (see
// docs/ARCHITECTURE.md).
// X-Frame-Options only governs whether THE PANEL can be framed by others,
// which it must not (clickjacking of write actions); it has no effect on the
// iframes the panel itself opens.
//
// hsts is only true on the built-in TLS listener: behind a reverse proxy the
// proxy owns HSTS, and sending it over plain HTTP is meaningless.
func SecurityHeaders(next http.Handler, hsts bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		if hsts {
			// No includeSubDomains: the panel is often one host among many
			// in a domain it does not own. Browsers ignore the header on a
			// connection with certificate errors, so the self-signed CA does
			// not lock anyone out.
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
