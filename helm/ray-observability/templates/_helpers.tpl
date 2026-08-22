{{- define "ray-observability.name" -}}
ray-observability
{{- end -}}

{{- define "ray-observability.labels" -}}
app.kubernetes.io/name: {{ include "ray-observability.name" . }}
app.kubernetes.io/part-of: ray-train-platform
app.kubernetes.io/managed-by: Helm
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
