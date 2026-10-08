{{- define "metrics.mtls.client" -}}
ca:
  secret: {name: {{ printf "obs-mtls-%s" .role }}, key: ca.crt}
cert:
  secret: {name: {{ printf "obs-mtls-%s" .role }}, key: tls.crt}
keySecret: {name: {{ printf "obs-mtls-%s" .role }}, key: tls.key}
serverName: {{ printf "%s.%s.svc" .peer .namespace | quote }}
insecureSkipVerify: false
{{- end -}}

{{- define "metrics.mtls.probe" -}}
httpGet:
  path: {{ .path | default "/healthz" }}
  port: 18081
  scheme: HTTP
periodSeconds: 10
timeoutSeconds: 2
failureThreshold: {{ .failureThreshold | default 6 }}
{{- end -}}

{{- define "metrics.mtls.sidecar" -}}
name: mtls-proxy
image: {{ .root.Values.mtls.image | quote }}
imagePullPolicy: IfNotPresent
{{- if eq .role "alertmanager" }}
# listenLocal removes AM's generated web containerPort; the proxy owns it.
ports:
  - {name: web, containerPort: 9093, protocol: TCP}
{{- end }}
command: [/bin/sh, -ec]
args:
  - |
    case "$POD_IP" in *[!0-9.]*|'') exit 1;; esac
    sed "s/__POD_IP__/$POD_IP/g" /etc/envoy-source/envoy.yaml > /var/run/envoy/envoy.yaml
    exec /usr/local/bin/envoy -c /var/run/envoy/envoy.yaml --concurrency 1 --disable-hot-restart --log-level warning
env:
  - name: POD_IP
    valueFrom: {fieldRef: {fieldPath: status.podIP}}
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  runAsUser: 65534
  runAsGroup: 65534
  capabilities: {drop: [ALL]}
  seccompProfile: {type: RuntimeDefault}
resources: {{ toJson .root.Values.mtls.resources }}
volumeMounts:
  - {name: mtls-identity, mountPath: /etc/obs-mtls, readOnly: true}
  - {name: mtls-config, mountPath: /etc/envoy-source, readOnly: true}
  - {name: mtls-runtime, mountPath: /var/run/envoy}
readinessProbe:
  {{- include "metrics.mtls.probe" . | nindent 2 }}
startupProbe:
  {{- include "metrics.mtls.probe" (dict "path" "/livez" "failureThreshold" 30) | nindent 2 }}
livenessProbe:
  {{- include "metrics.mtls.probe" (dict "path" "/livez" "failureThreshold" 3) | nindent 2 }}
{{- end -}}

{{- define "metrics.mtls.pod" -}}
securityContext:
  runAsNonRoot: true
  runAsUser: 65534
  runAsGroup: 65534
  fsGroup: 65534
  seccompProfile: {type: RuntimeDefault}
volumes:
  - name: mtls-identity
    secret: {secretName: {{ printf "obs-mtls-%s" .role }}, defaultMode: 0440}
  - name: mtls-config
    configMap: {name: {{ printf "%s-%s-mtls" .root.Values.name .role }}}
  - name: mtls-runtime
    emptyDir: {medium: Memory, sizeLimit: 8Mi}
containers:
  - {{- include "metrics.mtls.sidecar" . | nindent 4 }}
  {{- if .reloader }}
  - name: config-reloader
    readinessProbe:
      {{- include "metrics.mtls.probe" . | nindent 6 }}
    livenessProbe:
      {{- include "metrics.mtls.probe" (dict "path" "/reloader-healthz" "failureThreshold" 12) | nindent 6 }}
  {{- end }}
readinessProbe:
  {{- include "metrics.mtls.probe" . | nindent 2 }}
livenessProbe:
  {{- include "metrics.mtls.probe" (dict "path" "/app-healthz" "failureThreshold" 12) | nindent 2 }}
startupProbe:
  {{- include "metrics.mtls.probe" (dict "failureThreshold" 60) | nindent 2 }}
{{- if .reloader }}
configReloaderExtraArgs:
  http.listenAddr: "127.0.0.1:8435"
  reload-url: {{ printf "http://127.0.0.1:%v/-/reload" .port | quote }}
{{- end }}
{{- end -}}

{{- define "metrics.mtls.storageNodes" -}}
{{- $nodes := list -}}
{{- range $index := until (int .root.Values.storage.replicas) -}}
{{- $nodes = append $nodes (printf "127.0.0.1:%d" (add (int $.base) $index)) -}}
{{- end -}}
{{- join "," $nodes -}}
{{- end -}}
