{{- define "ray-train-platform.imagePullSecretNames" -}}
{{- $names := list -}}
{{- range .Values.global.imagePullSecrets -}}
{{- $names = append $names .name -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}
