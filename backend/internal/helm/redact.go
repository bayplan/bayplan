package helm

import (
	"github.com/bayplan/bayplan/internal/k8s"
	"strings"

	"sigs.k8s.io/yaml"
)

// redactedMarker is the same text k8s.ResourceYAML uses for Secrets.
const redactedMarker = "<redacted>"

// RedactSecrets replaces the data/stringData values of the Secrets in a
// manifest rendered by Helm. A release's manifest usually carries the Secrets
// the chart creates (DB passwords, API keys), and release reads go through the
// technical SA, so the cluster's RBAC does not guard against a readonly user:
// the same rule as a Secret's YAML (only admin sees the values) applies here.
// Documents that are not a Secret are returned as is, with their comments.
func RedactSecrets(manifest string) (out string, redacted int) {
	docs := strings.Split(manifest, "\n---")
	for i, doc := range docs {
		if !strings.Contains(doc, "kind: Secret") {
			continue
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj["kind"] != "Secret" {
			continue
		}
		changed := false
		for _, field := range []string{"data", "stringData"} {
			if m, ok := obj[field].(map[string]any); ok {
				for k := range m {
					m[k] = redactedMarker
					changed = true
				}
			}
		}
		if !changed {
			continue
		}
		if meta, ok := obj["metadata"].(map[string]any); ok {
			if ann, ok := meta["annotations"].(map[string]any); ok {
				for k, v := range ann {
					if s, _ := v.(string); k8s.IsSecretCopyAnnotation(k, s) {
						delete(ann, k)
					}
				}
			}
		}
		b, err := yaml.Marshal(obj)
		if err != nil {
			continue
		}
		// Keep the leading newline that separates it from the previous "---".
		prefix := ""
		if strings.HasPrefix(doc, "\n") {
			prefix = "\n"
		}
		docs[i] = prefix + string(b)
		redacted++
	}
	return strings.Join(docs, "\n---"), redacted
}
