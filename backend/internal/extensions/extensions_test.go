package extensions

import (
	"strings"
	"testing"

	"github.com/bayplan/bayplan/internal/rbac"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want string // error fragment, "" = valid
	}{
		{"ok simple", Input{Name: "Grafana", URL: "https://grafana.example.com/d/abc"}, ""},
		{"ok placeholders", Input{Name: "Logs", URL: "https://kibana.example.com/app/discover?cluster={cluster}&ns={namespace}&id={clusterId}", Mode: ModeEmbed, MinRole: rbac.RoleEditor}, ""},
		{"empty name", Input{Name: "  ", URL: "https://x.example"}, "name"},
		{"empty url", Input{Name: "x", URL: ""}, "url"},
		{"javascript", Input{Name: "x", URL: "javascript:alert(1)"}, "http"},
		{"relative", Input{Name: "x", URL: "/local/path"}, "http"},
		{"unknown placeholder", Input{Name: "x", URL: "https://x.example/{typo}"}, "unknown placeholder"},
		{"unclosed brace", Input{Name: "x", URL: "https://x.example/{cluster"}, "unclosed"},
		{"invalid mode", Input{Name: "x", URL: "https://x.example", Mode: "popup"}, "mode"},
		{"invalid role", Input{Name: "x", URL: "https://x.example", MinRole: "root"}, "minRole"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(&c.in)
			if c.want == "" {
				if err != nil {
					t.Fatalf("expected valid, got error: %v", err)
				}
				if c.in.Mode == "" || c.in.MinRole == "" {
					t.Fatalf("defaults were not applied: %+v", c.in)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestExpand(t *testing.T) {
	got := Expand("https://k.example/app?c={cluster}&id={clusterId}&ns={namespace}", 7, "prod eu", "kube-system")
	want := "https://k.example/app?c=prod%20eu&id=7&ns=kube-system"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
