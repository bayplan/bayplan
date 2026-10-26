package ai

import (
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// What leaves for a third party is decided here, and it errs on the side of
// sending less: the model explains a CrashLoopBackOff just as well without the
// database password that sits in an env var of the same Deployment.

const redacted = "<redacted>"

// RedactManifest takes an object's YAML (already with Secret values masked by
// the panel) and removes what has no diagnostic value and may be sensitive:
// literal env values (names and valueFrom references stay, they explain wiring),
// the last-applied annotation (a full copy of the spec, env included),
// managedFields, a Secret's data and a ConfigMap's values (keys stay).
// maxAnnotationBytes: anything longer is a serialized object, not a label.
const maxAnnotationBytes = 1024

func RedactManifest(manifest string) (string, error) {
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(manifest), &obj); err != nil {
		return "", err
	}
	switch kind, _ := obj["kind"].(string); kind {
	case "Secret":
		delete(obj, "data")
		delete(obj, "stringData")
	case "ConfigMap":
		// Keys stay (they explain what a workload mounts); values often are
		// connection strings or whole config files with credentials.
		for _, field := range []string{"data", "binaryData"} {
			if values, ok := obj[field].(map[string]any); ok {
				for k := range values {
					values[k] = redacted
				}
			}
		}
	}
	if meta, ok := obj["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
		if ann, ok := meta["annotations"].(map[string]any); ok {
			delete(ann, "kubectl.kubernetes.io/last-applied-configuration")
			// Other tools keep their own copy of the object (env values
			// included), encoded in ways the key/value heuristic never sees.
			delete(ann, "objectset.rio.cattle.io/applied")
			delete(ann, "kapp.k14s.io/original")
			for k, v := range ann {
				if s, ok := v.(string); ok && len(s) > maxAnnotationBytes {
					ann[k] = redacted
				}
			}
		}
	}
	redactEnv(obj)
	out, err := yaml.Marshal(obj)
	if err != nil {
		return "", err
	}
	return RedactText(string(out)), nil
}

// redactEnv walks the whole object: env lists live at different depths in a
// Pod, a Deployment's template, a CronJob's jobTemplate…
func redactEnv(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if k == "env" {
				if list, ok := child.([]any); ok {
					for _, item := range list {
						if env, ok := item.(map[string]any); ok {
							if _, has := env["value"]; has {
								env["value"] = redacted
							}
						}
					}
					continue
				}
			}
			redactEnv(child)
		}
	case []any:
		for _, child := range t {
			redactEnv(child)
		}
	}
}

var textRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// PEM blocks (private keys, certificates with keys bundled).
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), redacted},
	// Authorization headers and bearer tokens in logs.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 " + redacted},
	// JWTs anywhere.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), redacted},
	// Credentials embedded in URLs: scheme://user:password@host.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):[^\s@/]+@`), "$1:" + redacted + "@"},
	// AWS access keys.
	{regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`), redacted},
	// key=value / key: value where the key names a secret.
	{regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(password|passwd|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|credentials?)[a-z0-9_.-]*)(["']?\s*[:=]\s*["']?)[^\s"',;}&]+`), "$1$3" + redacted},
}

// RedactText scrubs secret-looking values from free text (logs, events,
// rendered YAML). It is a heuristic and says so in the UI: it catches the
// usual shapes, not every possible secret.
func RedactText(s string) string {
	for _, r := range textRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// Clip keeps the END of a long text (the newest log lines, the status at the
// bottom of a YAML) and says it was cut.
func Clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < 200 {
		cut = cut[i+1:]
	}
	return "[… earlier content cut …]\n" + cut
}
