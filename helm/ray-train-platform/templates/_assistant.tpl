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
{{- $item := dict "id" $provider.id "kind" $provider.kind "baseURL" (required "assistant provider baseURL required" $provider.baseURL) "model" (required "assistant provider model required" $provider.model) "thinkingDisabled" (default false $provider.thinkingDisabled) -}}
{{- if or (eq $provider.kind "api") $provider.existingSecret -}}
{{- $_ := required "assistant provider existingSecret required" $provider.existingSecret -}}
{{- $_ := required "assistant provider secretKey required" $provider.secretKey -}}
{{- $_ := set $item "keyEnv" (printf "ASSISTANT_PROVIDER_%s_KEY" (upper (replace "-" "_" $provider.id))) -}}
{{- end -}}
{{- $serialized = append $serialized $item -}}
{{- end -}}
- name: ASSISTANT_ENABLED
  value: "true"
- name: ASSISTANT_LOCAL_FIRST
  value: {{ default false $config.localFirst | quote }}
- name: ASSISTANT_PROVIDERS
  value: {{ $serialized | toJson | quote }}
{{- range $provider := $providers }}
{{- if or (eq $provider.kind "api") $provider.existingSecret }}
- name: {{ printf "ASSISTANT_PROVIDER_%s_KEY" (upper (replace "-" "_" $provider.id)) }}
  valueFrom:
    secretKeyRef:
      name: {{ $provider.existingSecret | quote }}
      key: {{ $provider.secretKey | quote }}
{{- end }}
{{- end }}
{{- end -}}
