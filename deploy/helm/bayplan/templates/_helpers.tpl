{{- define "bayplan.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "bayplan.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "bayplan.labels" -}}
app.kubernetes.io/name: {{ include "bayplan.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "bayplan.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bayplan.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
withRegistry: an image reference with its registry replaced by imageRegistry
(air-gapped installs). The first path segment is a registry only if it has a
dot, a colon or is localhost, the same rule as the container runtimes.
*/}}
{{- define "bayplan.withRegistry" -}}
{{- $ctx := index . 0 -}}
{{- $ref := index . 1 -}}
{{- if $ctx.Values.imageRegistry -}}
{{- $parts := splitList "/" $ref -}}
{{- $first := first $parts -}}
{{- if and (gt (len $parts) 1) (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- $ref = join "/" (rest $parts) -}}
{{- end -}}
{{- printf "%s/%s" (trimSuffix "/" $ctx.Values.imageRegistry) $ref -}}
{{- else -}}
{{- $ref -}}
{{- end -}}
{{- end -}}

{{- define "bayplan.image" -}}
{{- include "bayplan.withRegistry" (list . (printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag))) -}}
{{- end -}}

{{/* The agent image the server writes into the manifests. */}}
{{- define "bayplan.agentImage" -}}
{{- if .Values.agentImage -}}
{{- .Values.agentImage -}}
{{- else -}}
{{- include "bayplan.withRegistry" (list . (printf "ghcr.io/bayplan/bayplan-agent:%s" .Chart.AppVersion)) -}}
{{- end -}}
{{- end -}}

{{/* Secret holding the ingress certificate. */}}
{{- define "bayplan.tlsSecretName" -}}
{{- default (printf "%s-tls" (include "bayplan.fullname" .)) .Values.ingress.tls.secretName -}}
{{- end -}}

{{- define "bayplan.publicURL" -}}
{{- if .Values.publicURL -}}
{{- .Values.publicURL -}}
{{- else if .Values.ingress.enabled -}}
{{- printf "%s://%s" (ternary "https" "http" .Values.ingress.tls.enabled) .Values.ingress.host -}}
{{- end -}}
{{- end -}}

{{/*
Value stable across upgrades: if the Secret already exists its value is reused;
otherwise the given one is used or one is generated. Without this, every `helm
upgrade` would rotate the encryption key and make every stored credential unreadable.
*/}}
{{- define "bayplan.stableSecretValue" -}}
{{- $ctx := index . 0 -}}
{{- $secretName := index . 1 -}}
{{- $key := index . 2 -}}
{{- $given := index . 3 -}}
{{- $existing := lookup "v1" "Secret" $ctx.Release.Namespace $secretName -}}
{{- if and $existing (index $existing.data $key) -}}
{{- index $existing.data $key | b64dec -}}
{{- else if $given -}}
{{- $given -}}
{{- else -}}
{{- randAlphaNum 32 -}}
{{- end -}}
{{- end -}}
