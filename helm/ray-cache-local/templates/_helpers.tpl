{{- define "ray-cache-local.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ray-cache-local.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- include "ray-cache-local.name" . -}}
{{- end -}}
{{- end -}}

{{- define "ray-cache-local.labels" -}}
app.kubernetes.io/name: {{ include "ray-cache-local.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: ray-train-platform
{{- end -}}

{{/* YAML scalar: quote external names (including numeric/boolean names), preserve legacy output. */}}
{{- define "ray-cache-local.configMapName" -}}
{{- $name := .Values.existingConfigMap -}}
{{- if ne $name nil -}}
{{- if not (kindIs "string" $name) -}}
{{- fail "existingConfigMap must be a string" -}}
{{- end -}}
{{- end -}}
{{- if $name -}}
{{- if or (gt (len $name) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $name)) -}}
{{- fail "existingConfigMap must be a valid DNS subdomain of at most 253 characters" -}}
{{- end -}}
{{- $name | quote -}}
{{- else -}}
{{- include "ray-cache-local.fullname" . -}}-config
{{- end -}}
{{- end -}}

{{- define "ray-cache-local.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ray-cache-local.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "ray-cache-local.requireDigestImage" -}}
{{- $image := . -}}
{{- if not (regexMatch "^[^[:space:]@]+@sha256:[a-f0-9]{64}$" $image) -}}
{{- fail (printf "image must use repository@sha256:digest: %s" $image) -}}
{{- end -}}
{{- $image -}}
{{- end -}}
