package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerNoListingAndCaching(t *testing.T) {
	h, err := Handler()
	if err != nil {
		t.Fatal(err)
	}
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}
	for _, p := range []string{"/", "/clusters/1/pods", "/index.html"} {
		rec := get(p)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: code=%d cache=%q", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
	for _, p := range []string{"/assets/", "/assets", "/assets/does-not-exist.js"} {
		rec := get(p)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<pre>") {
			t.Errorf("%s: a directory or missing asset must be a 404 without a listing, got %d", p, rec.Code)
		}
	}
}
