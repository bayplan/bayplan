package session

import (
	"net/http"
	"testing"
)

func TestBearerTokenOnlyAcceptsOurPrefix(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	if bearerToken(r) != "" {
		t.Fatal("no header means no token")
	}
	r.Header.Set("Authorization", "Bearer eyJhbGciOi.jwt.foreign")
	if bearerToken(r) != "" {
		t.Fatal("a Bearer that is not ours is ignored (the cookie is used instead)")
	}
	r.Header.Set("Authorization", "Bearer bpl_abc")
	if bearerToken(r) != "bpl_abc" {
		t.Fatal("own token not recognized")
	}
}
