package k8s

import corev1 "k8s.io/api/core/v1"

// secretCopyAnnotations hold a serialized copy of the object, values
// included: kubectl's last-applied (stringData in clear when the Secret was
// created with `kubectl apply`), objectset's applied and kapp's original.
// Masking data/stringData while leaving them would mask nothing.
var secretCopyAnnotations = map[string]bool{
	"kubectl.kubernetes.io/last-applied-configuration": true,
	"objectset.rio.cattle.io/applied":                  true,
	"kapp.k14s.io/original":                            true,
}

// maxSecretAnnotationBytes: on a Secret, an annotation longer than this is a
// serialized object from some tool the list above does not know, not a label.
const maxSecretAnnotationBytes = 1024

// IsSecretCopyAnnotation reports whether a masked Secret must drop this
// annotation. Shared by the YAML view, the Helm manifest and the backup dump.
func IsSecretCopyAnnotation(key, value string) bool {
	return secretCopyAnnotations[key] || len(value) > maxSecretAnnotationBytes
}

// redactSecret masks a Secret's values in place (the caller passes a copy)
// and drops the annotations that would carry them anyway.
func redactSecret(sec *corev1.Secret) {
	for key := range sec.Data {
		sec.Data[key] = []byte(redactedMarker)
	}
	for key := range sec.StringData {
		sec.StringData[key] = redactedMarker
	}
	for k, v := range sec.Annotations {
		if IsSecretCopyAnnotation(k, v) {
			delete(sec.Annotations, k)
		}
	}
}
