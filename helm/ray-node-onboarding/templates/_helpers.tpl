{{- define "node-onboarding.validate" -}}
{{- if or (not (kindIs "bool" .Values.enabled)) (not (kindIs "bool" .Values.activateController)) -}}{{- fail "enabled and activateController must be booleans" -}}{{- end -}}
{{- if ne .Release.Namespace "ray-cache-local" -}}{{- fail "install node onboarding only in the existing ray-cache-local namespace" -}}{{- end -}}
{{- range $image := list .Values.image .Values.helperImage -}}
{{- if not (kindIs "string" $image) -}}{{- fail "images must be digest-pinned strings" -}}{{- end -}}
{{- if not (regexMatch "^[^[:space:]@]+@sha256:[a-f0-9]{64}$" $image) -}}{{- fail "images must be digest-pinned repository@sha256 references" -}}{{- end -}}
{{- end -}}
{{- range $name := list .Values.data1ConfigMap .Values.data2ConfigMap .Values.data1StorageClass .Values.data2StorageClass -}}
{{- if not (kindIs "string" $name) -}}{{- fail "config and storage class names must be strings" -}}{{- end -}}
{{- if or (gt (len $name) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $name)) -}}{{- fail "config and storage class names must be DNS subdomains" -}}{{- end -}}
{{- end -}}
{{- if or (eq .Values.data1ConfigMap .Values.data2ConfigMap) (eq .Values.data1StorageClass .Values.data2StorageClass) -}}{{- fail "data1 and data2 require distinct maps and storage classes" -}}{{- end -}}
{{- range $name := list .Values.data1ConfigMap .Values.data2ConfigMap -}}
{{- if or (eq $name "ray-cache-local-data1-config") (eq $name "ray-cache-local-data2-config") (eq $name "node-onboarding-nfs") (eq $name "node-onboarding-state") -}}{{- fail "external maps must not be legacy Helm maps, proof store or the NFS configuration" -}}{{- end -}}
{{- end -}}
{{- if not (kindIs "slice" .Values.nfs.shares) -}}{{- fail "nfs.shares must be a list" -}}{{- end -}}
{{- if or (lt (len .Values.nfs.shares) 1) (gt (len .Values.nfs.shares) 8) -}}{{- fail "nfs.shares must contain 1..8 fixed read-only sources" -}}{{- end -}}
{{- range $share := .Values.nfs.shares -}}
{{- if or (not (kindIs "map" $share)) (ne (len $share) 2) -}}{{- fail "NFS sources accept only server and path" -}}{{- end -}}
{{- if or (not (kindIs "string" $share.server)) (not (kindIs "string" $share.path)) -}}{{- fail "NFS server and path must be strings" -}}{{- end -}}
{{- if or (gt (len $share.server) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $share.server)) -}}{{- fail "NFS server must be a DNS name or IPv4 address" -}}{{- end -}}
{{- if or (not (regexMatch "^/([A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+$" $share.path)) (regexMatch "(^|/)\\.\\.?(/|$)" $share.path) -}}{{- fail "NFS path must be an absolute clean export path" -}}{{- end -}}
{{- end -}}
{{- end -}}

{{- define "node-onboarding.probeScript" -}}
set -eu; umask 077; for d in /cache1 /cache2; do printf '%s' onboarding-probe > "$d/marker"; test "$(cat "$d/marker")" = onboarding-probe; rm "$d/marker"; done
{{- range $i, $share := .Values.nfs.shares -}}; test -r /nfs-{{ $i }}/.{{- end -}}
{{- end -}}
