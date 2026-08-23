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

{{- /* Every platform workload stays off VCI. Cluster profiles can prefer a
physical CPU pool and either permit physical GPU fallback or make the same
selector required. Component nodeSelector values remain an explicit escape
hatch and are rendered separately by each workload template. */ -}}
{{- define "ray-train-platform.nodeAffinity" -}}
{{- $placement := default (dict) .Values.placement -}}
{{- $preferred := default (dict) (get $placement "preferredNodeSelector") -}}
{{- $allowFallback := default false (get $placement "allowGPUNodeFallback") -}}
nodeAffinity:
  requiredDuringSchedulingIgnoredDuringExecution:
    nodeSelectorTerms:
      - matchExpressions:
          - key: node.kubernetes.io/instance-type
            operator: NotIn
            values: ["virtual-node"]
          - key: type
            operator: NotIn
            values: ["virtual-kubelet"]
          {{- if and $preferred (not $allowFallback) }}
          {{- range $key, $value := $preferred }}
          - key: {{ $key }}
            operator: In
            values: [{{ $value | quote }}]
          {{- end }}
          {{- end }}
{{- if and $preferred $allowFallback }}
  preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      preference:
        matchExpressions:
          {{- range $key, $value := $preferred }}
          - key: {{ $key }}
            operator: In
            values: [{{ $value | quote }}]
          {{- end }}
{{- end }}
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
