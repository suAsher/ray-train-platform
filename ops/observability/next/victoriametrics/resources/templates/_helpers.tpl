{{- define "metrics.guard" -}}
{{- if ne .Release.Namespace .Values.expectedNamespace -}}
{{- fail "This chart may only be installed in values.expectedNamespace" -}}
{{- end -}}
{{- if ne .Values.dedupInterval .Values.scrapeInterval -}}
{{- fail "Initial self-monitoring dedupInterval must equal scrapeInterval" -}}
{{- end -}}
{{- end -}}

{{- define "metrics.labels" -}}
app.kubernetes.io/part-of: raytrain-observability-next
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
{{- end -}}

{{- define "metrics.scheduling" -}}
priorityClassName: {{ .root.Values.priorityClassName | quote }}
nodeSelector: {{ toJson .root.Values.scheduling.nodeSelector }}
tolerations: {{ toJson .root.Values.scheduling.tolerations }}
affinity:
  nodeAffinity:
    {{- with .root.Values.scheduling.excludedHostnames }}
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - key: kubernetes.io/hostname
              operator: NotIn
              values: {{ toJson . }}
    {{- end }}
    {{- with .root.Values.scheduling.preferredHostnames }}
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 50
        preference:
          matchExpressions:
            - key: kubernetes.io/hostname
              operator: In
              values: {{ toJson . }}
    {{- end }}
  podAntiAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      - topologyKey: kubernetes.io/hostname
        labelSelector:
          matchLabels:
            observability.raytrain.io/component: {{ .component }}
{{- end -}}

{{- define "metrics.common" -}}
podMetadata:
  labels:
    observability.raytrain.io/component: {{ .component }}
  annotations:
    observability.raytrain.io/envoy-config-sha256: {{ printf "%s\n" (include "metrics.mtls.config" (dict "root" .root "role" .component) | fromJson | toYaml) | sha256sum | quote }}
disableSelfServiceScrape: true
useStrictSecurity: true
enableServiceLinks: false
{{ include "metrics.scheduling" . }}
{{- end -}}
