package dockerenv

import (
	"strings"
	"testing"
)

func TestMaskInspect(t *testing.T) {
	raw := []byte(`{"Id":"abc","Config":{"Image":"postgres","Env":["POSTGRES_PASSWORD=devlocal","PATH=/bin","NOVALUE"]},"HostConfig":{}}`)
	out, err := MaskInspect(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "devlocal") || strings.Contains(s, "/bin") {
		t.Fatalf("value leaked: %s", s)
	}
	for _, want := range []string{`"POSTGRES_PASSWORD=redacted"`, `"PATH=redacted"`, `"NOVALUE=redacted"`, `"Image":"postgres"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if _, err := MaskInspect([]byte("not json")); err == nil {
		t.Error("expected an error on invalid JSON")
	}
}
