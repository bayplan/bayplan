package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShouldAudit(t *testing.T) {
	cases := []struct {
		name   string
		method string
		status int
		want   bool
	}{
		{"403 on DELETE is audited", http.MethodDelete, http.StatusForbidden, true},
		{"403 on POST is audited", http.MethodPost, http.StatusForbidden, true},
		{"403 on PATCH is audited", http.MethodPatch, http.StatusForbidden, true},
		// The duplicate-name 409s (user or cluster) travel through the same
		// chain as this middleware: auditing them as a denial would be a false
		// positive. The 409s of the guards are audited by each handler.
		{"409 is not audited", http.MethodDelete, http.StatusConflict, false},
		{"401 is not audited", http.MethodPost, http.StatusUnauthorized, false},
		{"400 is not audited", http.MethodPost, http.StatusBadRequest, false},
		{"204 is not audited", http.MethodDelete, http.StatusNoContent, false},
		{"403 on GET is not audited", http.MethodGet, http.StatusForbidden, false},
		{"403 on HEAD is not audited", http.MethodHead, http.StatusForbidden, false},
		{"403 on OPTIONS is not audited", http.MethodOptions, http.StatusForbidden, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldAudit(c.method, c.status); got != c.want {
				t.Errorf("shouldAudit(%q, %d) = %v, expected %v", c.method, c.status, got, c.want)
			}
		})
	}
}

// The default status must be 200: a handler that responds without calling
// WriteHeader cannot end up classified as a denial.
func TestDeniedRecorderDefaultStatus(t *testing.T) {
	rec := &deniedRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	if _, err := rec.Write([]byte("ok")); err != nil {
		t.Fatalf("Write returned an error: %v", err)
	}
	if rec.status != http.StatusOK {
		t.Errorf("status = %d, expected 200", rec.status)
	}
	if shouldAudit(http.MethodPost, rec.status) {
		t.Error("a 200 response must not be audited")
	}
}

func TestDeniedRecorderCapturesTheReason(t *testing.T) {
	rec := &deniedRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	http.Error(rec, "admin role required", http.StatusForbidden)

	if rec.status != http.StatusForbidden {
		t.Fatalf("status = %d, expected 403", rec.status)
	}
	if got := rec.body; got != "admin role required\n" {
		t.Errorf("body = %q, expected the http.Error message", got)
	}
}

// The body is bounded: audit_log.detail must not swallow a large response.
func TestDeniedRecorderCapsTheReason(t *testing.T) {
	rec := &deniedRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	rec.WriteHeader(http.StatusForbidden)
	long := make([]byte, maxReasonLen*3)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := rec.Write(long); err != nil {
		t.Fatalf("Write returned an error: %v", err)
	}
	if len(rec.body) != maxReasonLen {
		t.Errorf("len(body) = %d, expected %d", len(rec.body), maxReasonLen)
	}
}

// The body of a response that is NOT a 403 is not kept: only the reason for
// the denial matters.
func TestDeniedRecorderIgnoresBodyUnless403(t *testing.T) {
	rec := &deniedRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	http.Error(rec, "a user with that name already exists", http.StatusConflict)
	if rec.body != "" {
		t.Errorf("body = %q, expected empty", rec.body)
	}
}
