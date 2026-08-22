{{- define "ray-train-platform.imagePullSecretNames" -}}
{{- $names := list -}}
{{- range .Values.global.imagePullSecrets -}}
{{- $names = append $names .name -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}

{{- define "ray-train-platform.name" -}}
ray-train-platform
{{- end -}}

{{- define "ray-train-platform.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "ray-train-platform.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ray-train-platform.commonLabels" -}}
app.kubernetes.io/name: {{ include "ray-train-platform.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: ray-train-platform
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- /* Production profiles set digest; test profiles can intentionally use a
release tag while exercising image build and deployment flow. */}}
{{- define "ray-train-platform.image" -}}
{{- $root := index . 0 -}}
{{- $image := index . 1 -}}
{{- $repository := required "image.repository is required" (get $image "repository") -}}
{{- $digest := default "" (get $image "digest") -}}
{{- if $digest -}}
{{- printf "%s/%s@%s" $root.Values.global.imageRegistry $repository $digest -}}
{{- else -}}
{{- printf "%s/%s:%s" $root.Values.global.imageRegistry $repository (required "image.tag is required" (get $image "tag")) -}}
{{- end -}}
{{- end -}}
