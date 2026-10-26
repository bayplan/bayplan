package api

import (
	"strings"
	"testing"
)

func TestQuantity(t *testing.T) {
	into := map[string]any{}
	if err := quantity("requestsCpu", " 500m ", into, "requests.cpu"); err != nil || into["requests.cpu"] != "500m" {
		t.Fatalf("500m: %v %v", err, into)
	}
	if err := quantity("requestsMemory", "4Gi", into, "requests.memory"); err != nil || into["requests.memory"] != "4Gi" {
		t.Fatalf("4Gi: %v %v", err, into)
	}
	// Empty = no cap: does not write the key and is not an error.
	if err := quantity("pods", "", into, "pods"); err != nil {
		t.Fatal(err)
	}
	if _, ok := into["pods"]; ok {
		t.Fatal("empty must not write the key")
	}
	err := quantity("limitsCpu", "two", into, "limits.cpu")
	if err == nil || !strings.Contains(err.Error(), "limitsCpu") {
		t.Fatalf("expected an error with the field name, got %v", err)
	}
}

func TestGitopsURLRegexes(t *testing.T) {
	for _, ok := range []string{"git@github.com:org/repo.git", "deploy@gitea.internal:group/app"} {
		if !scpLikeRe.MatchString(ok) {
			t.Errorf("%q should be a valid scp URL", ok)
		}
	}
	for _, bad := range []string{"https://github.com/org/repo.git", "git@github.com", "git@host:with space"} {
		if scpLikeRe.MatchString(bad) {
			t.Errorf("%q should not be a valid scp URL", bad)
		}
	}
	if !sourceNameRe.MatchString("my-app-2") || sourceNameRe.MatchString("My App") {
		t.Error("sourceNameRe")
	}
}
