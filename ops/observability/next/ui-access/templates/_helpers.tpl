{{- define "ui.validate" -}}
{{- if ne .Release.Namespace "nightingale" }}{{ fail "UI gateway must be installed in nightingale" }}{{ end -}}
{{- if lt (int .Values.replicas) 2 }}{{ fail "UI gateway requires at least two replicas" }}{{ end -}}
{{- if not (has .Values.service.type (list "NodePort" "ClusterIP")) }}{{ fail "service.type must be NodePort or ClusterIP" }}{{ end -}}
{{- if eq (int .Values.service.nightingaleNodePort) (int .Values.service.grafanaNodePort) }}{{ fail "NodePorts must be distinct" }}{{ end -}}
{{- range $port := list .Values.service.nightingaleNodePort .Values.service.grafanaNodePort -}}
{{- if or (lt (int $port) 30000) (gt (int $port) 32767) }}{{ fail "NodePort must be within 30000-32767" }}{{ end -}}
{{- end -}}
{{- if not (regexMatch "^harbor[.]wellspiking[.]ai/hub/envoyproxy/envoy:v[0-9]+[.][0-9]+[.][0-9]+@sha256:[a-f0-9]{64}$" .Values.image) }}{{ fail "Envoy requires a reviewed Harbor tag and immutable digest" }}{{ end -}}
{{- if not .Values.certificate.ipAddresses }}{{ fail "At least one private node IP SAN is required" }}{{ end -}}
{{- range $ip := .Values.certificate.ipAddresses -}}
{{- if not (regexMatch "^[0-9]{1,3}([.][0-9]{1,3}){3}$" $ip) }}{{ fail "Certificate SAN must be an IPv4 address" }}{{ end -}}
{{- $octets := splitList "." $ip -}}
{{- range $octet := $octets -}}
{{- if or (gt (int $octet) 255) (ne (toString (int $octet)) $octet) }}{{ fail "Certificate SAN contains an invalid IPv4 octet" }}{{ end -}}
{{- end -}}
{{- $a := int (index $octets 0) -}}{{- $b := int (index $octets 1) -}}
{{- if not (or (eq $a 10) (and (eq $a 172) (ge $b 16) (le $b 31)) (and (eq $a 192) (eq $b 168))) }}{{ fail "NodePort browser certificate SAN must be a private IPv4 address" }}{{ end -}}
{{- end -}}
{{- end -}}

{{- define "ui.labels" -}}
app.kubernetes.io/name: observability-ui
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: raytrain-observability-next
{{- end -}}

{{- define "ui.selector" -}}
app.kubernetes.io/name: observability-ui
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
