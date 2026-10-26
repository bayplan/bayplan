package middleware

import (
	"net/http"

	"github.com/bayplan/bayplan/internal/session"
)

// RequireCSRF requires mutating requests to include the X-CSRF-Token header
// with the same value as the bayplan_csrf cookie (double-submit pattern).
// Defense in depth on top of SameSite=Lax: an external site cannot read the
// cookie from the victim's browser to replay the value in the header.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// With an API token there is no session cookie a foreign site could
		// make the browser send, so there is no CSRF to defend against;
		// requiring the header would only break scripts.
		if sess, ok := SessionFromContext(r.Context()); ok && sess.ViaToken {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(session.CSRFCookieName)
		if err != nil || cookie.Value == "" || cookie.Value != r.Header.Get("X-CSRF-Token") {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
