package k8s

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRedactSecretDropsCopies(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "s",
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": `{"stringData":{"pw":"topsecret"}}`,
				"objectset.rio.cattle.io/applied":                  "H4sItopsecret",
				"kapp.k14s.io/original":                            `{"data":{"pw":"dG9wc2VjcmV0"}}`,
				"some.tool/blob":                                   strings.Repeat("x", 2000),
				"owner":                                            "team-a",
			},
		},
		Data:       map[string][]byte{"pw": []byte("topsecret")},
		StringData: map[string]string{"token": "abc"},
	}
	redactSecret(sec)
	if string(sec.Data["pw"]) != redactedMarker || sec.StringData["token"] != redactedMarker {
		t.Fatalf("values not masked: %v %v", sec.Data, sec.StringData)
	}
	if len(sec.Annotations) != 1 || sec.Annotations["owner"] != "team-a" {
		t.Fatalf("copy annotations must go and the rest stay: %v", sec.Annotations)
	}
}
