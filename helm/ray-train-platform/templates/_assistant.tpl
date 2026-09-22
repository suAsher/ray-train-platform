{{/* Optional assistant: fixed compatible endpoints, credentials only through existing Secrets. */}}
{{- define "ray-train-platform.assistantEnv" -}}
{{- $config := .Values.assistant -}}
{{- $providers := default (list) $config.providers -}}
{{- if gt (len $providers) 4 }}{{- fail "assistant supports at most four configured providers" }}{{- end -}}
{{- $seen := dict -}}
{{- $serialized := list -}}
{{- range $provider := $providers -}}
{{- if not (regexMatch `^[a-z][a-z0-9-]{0,31}$` $provider.id) }}{{- fail "assistant provider id must be a short lowercase identifier" }}{{- end -}}
{{- if hasKey $seen $provider.id }}{{- fail "assistant provider ids must be unique" }}{{- end -}}
{{- $_ := set $seen $provider.id true -}}
{{- if not (has $provider.kind (list "api" "local")) }}{{- fail "assistant provider kind must be api or local" }}{{- end -}}
{{- $protocol := default "openai" $provider.protocol -}}
{{- if not (has $protocol (list "openai" "anthropic")) }}{{- fail "assistant protocol must be openai or anthropic" }}{{- end -}}
{{- if and (eq $protocol "anthropic") (default false $provider.thinkingDisabled) }}{{- fail "anthropic provider does not support thinkingDisabled" }}{{- end -}}
{{- $item := dict "id" $provider.id "kind" $provider.kind "protocol" $protocol "baseURL" (required "assistant provider baseURL required" $provider.baseURL) "model" (required "assistant provider model required" $provider.model) "thinkingDisabled" (default false $provider.thinkingDisabled) -}}
{{- if and (hasKey $provider "useSecretFiles") (not (kindIs "bool" $provider.useSecretFiles)) }}{{- fail "assistant useSecretFiles must be boolean" }}{{- end -}}
{{- if or (eq $provider.kind "api") $provider.existingSecret -}}
{{- $_ := required "assistant provider existingSecret required" $provider.existingSecret -}}
{{- $_ := required "assistant provider secretKey required" $provider.secretKey -}}
{{- if (default false $provider.useSecretFiles) -}}
{{- if or (ne $provider.kind "local") (not (hasPrefix "https://" $provider.baseURL)) }}{{- fail "assistant Secret files require a local HTTPS provider" }}{{- end -}}
{{- $_ := required "assistant file provider caSecretKey required" $provider.caSecretKey -}}
{{- $_ := set $item "keyFile" (printf "/var/run/secrets/raytrain-assistant/%s/token" $provider.id) -}}
{{- $_ := set $item "caFile" (printf "/var/run/secrets/raytrain-assistant/%s/ca.crt" $provider.id) -}}
{{- else -}}
{{- $_ := set $item "keyEnv" (printf "ASSISTANT_PROVIDER_%s_KEY" (upper (replace "-" "_" $provider.id))) -}}
{{- end -}}
{{- end -}}
{{- $serialized = append $serialized $item -}}
{{- end -}}
- name: ASSISTANT_ENABLED
  value: "true"
- name: ASSISTANT_PREVIEW_SUBJECTS
  value: {{ default (list) $config.previewSubjects | toJson | quote }}
- name: ASSISTANT_LOCAL_FIRST
  value: {{ default false $config.localFirst | quote }}
- name: ASSISTANT_PROVIDERS
  value: {{ $serialized | toJson | quote }}
{{- range $provider := $providers }}
{{- if and (not (default false $provider.useSecretFiles)) (or (eq $provider.kind "api") $provider.existingSecret) }}
- name: {{ printf "ASSISTANT_PROVIDER_%s_KEY" (upper (replace "-" "_" $provider.id)) }}
  valueFrom:
    secretKeyRef:
      name: {{ $provider.existingSecret | quote }}
      key: {{ $provider.secretKey | quote }}
{{- end }}
{{- end }}
{{- end -}}
