{{- define "business.guard" -}}
{{- if ne .Release.Namespace .Values.expectedNamespace -}}
{{- fail "business metrics must be installed in values.expectedNamespace" -}}
{{- end -}}
{{- if ne .Values.scrapeInterval "30s" -}}
{{- fail "business-wave1 scrapeInterval must stay 30s to match VM HA dedup" -}}
{{- end -}}
{{- if ne .Values.prometheusNamespace "monitoring" -}}
{{- fail "business-wave1 only adds monitors and dashboard to existing monitoring namespace" -}}
{{- end -}}
{{- end -}}

{{- define "business.labels" -}}
app.kubernetes.io/part-of: raytrain-observability-next
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
observability.raytrain.io/wave: business-wave1
{{- end -}}

{{- define "business.targetRelabelings" -}}
- action: keep
  sourceLabels: [__meta_kubernetes_service_name]
  regex: {{ .service | quote }}
- sourceLabels: [__meta_kubernetes_namespace]
  targetLabel: exporter_namespace
- sourceLabels: [__meta_kubernetes_pod_name]
  targetLabel: exporter_pod
- sourceLabels: [__meta_kubernetes_pod_node_name]
  targetLabel: exporter_node
{{- if .preserveWorkloadLabels }}
# Remove generated target names so honorLabels=false cannot overwrite workload
# namespace/pod/container/node labels emitted by KSM or DCGM.
- action: labeldrop
  regex: namespace|pod|container|node|service|endpoint
{{- end }}
- targetLabel: component
  replacement: {{ .component | quote }}
- targetLabel: telemetry_scope
  replacement: business-wave1
{{- end -}}

{{- define "business.metricRelabelings" -}}
- action: keep
  sourceLabels: [__name__]
  regex: {{ .metrics | quote }}
- action: labelkeep
  regex: {{ .labels | quote }}
{{- end -}}
