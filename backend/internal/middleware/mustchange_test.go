package middleware

import "testing"

func TestAllowedWhilePasswordChangePending(t *testing.T) {
	allowed := [][2]string{
		{"GET", "/api/auth/me"},
		{"POST", "/api/auth/change-password"},
		{"POST", "/api/auth/logout"},
		{"PATCH", "/api/auth/preferences"},
	}
	for _, c := range allowed {
		if !AllowedWhilePasswordChangePending(c[0], c[1]) {
			t.Errorf("%s %s must be allowed", c[0], c[1])
		}
	}
	denied := [][2]string{
		{"POST", "/api/auth/tokens"},
		{"GET", "/api/auth/tokens"},
		{"PATCH", "/api/auth/profile"},
		{"GET", "/api/clusters"},
		{"POST", "/api/users"},
		{"GET", "/api/auth/me/"},
		{"POST", "/api/auth/me"},
		{"GET", "/ws/clusters/1/kubectl"},
	}
	for _, c := range denied {
		if AllowedWhilePasswordChangePending(c[0], c[1]) {
			t.Errorf("%s %s must be blocked", c[0], c[1])
		}
	}
}
