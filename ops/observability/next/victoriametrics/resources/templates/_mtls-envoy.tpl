{{/* All helpers return JSON objects, so no credentials or imperative renderer is required. */}}
{{- define "metrics.mtls.uri" -}}
{{- printf "spiffe://observability.raytrain.local/%s/%s" .namespace .role -}}
{{- end -}}

{{- define "metrics.mtls.tls" -}}
{{- $sans := list -}}
{{- range .identities -}}
{{- $sans = append $sans (dict "san_type" "URI" "matcher" (dict "exact" .)) -}}
{{- end -}}
{{- $common := dict
  "tls_params" (dict "tls_minimum_protocol_version" "TLSv1_3")
  "tls_certificates" (list (dict "certificate_chain" (dict "filename" "/etc/obs-mtls/tls.crt") "private_key" (dict "filename" "/etc/obs-mtls/tls.key")))
  "validation_context" (dict "trusted_ca" (dict "filename" "/etc/obs-mtls/ca.crt") "match_typed_subject_alt_names" $sans) -}}
{{- $context := dict "common_tls_context" $common -}}
{{- if .server -}}
{{- $_ := set $context "@type" "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext" -}}
{{- $_ := set $context "require_client_certificate" true -}}
{{- else -}}
{{- $_ := set $context "@type" "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext" -}}
{{- end -}}
{{- dict "name" "envoy.transport_sockets.tls" "typed_config" $context | toJson -}}
{{- end -}}

{{- define "metrics.mtls.cluster" -}}
{{- $socket := dict "address" .host "port_value" (int .port) -}}
{{- $endpoint := dict "endpoint" (dict "address" (dict "socket_address" $socket)) -}}
{{- $cluster := dict "name" .name "connect_timeout" "3s" "type" "STATIC"
  "per_connection_buffer_limit_bytes" 262144
  "circuit_breakers" (dict "thresholds" (list (dict "max_connections" 128 "max_pending_requests" 128 "max_requests" 128 "max_retries" 1)))
  "load_assignment" (dict "cluster_name" .name "endpoints" (list (dict "lb_endpoints" (list $endpoint)))) -}}
{{- if .peer -}}
{{- $_ := set $cluster "type" "STRICT_DNS" -}}
{{- $_ := set $cluster "dns_lookup_family" "V4_ONLY" -}}
{{- $_ := set $cluster "respect_dns_ttl" true -}}
{{- $_ := set $cluster "transport_socket" (include "metrics.mtls.tls" (dict "identities" (list .peer) "server" false) | fromJson) -}}
{{- end -}}
{{- if .health -}}
{{- $_ := set $cluster "health_checks" (list (dict "timeout" "1s" "interval" "5s" "unhealthy_threshold" 2 "healthy_threshold" 1 "http_health_check" (dict "path" .health))) -}}
{{- end -}}
{{- toJson $cluster -}}
{{- end -}}

{{- define "metrics.mtls.listener" -}}
{{- $chain := dict "filters" .filters -}}
{{- if .identities -}}
{{- $_ := set $chain "transport_socket" (include "metrics.mtls.tls" (dict "identities" .identities "server" true) | fromJson) -}}
{{- end -}}
{{- dict "name" .name "address" (dict "socket_address" (dict "address" .host "port_value" (int .port)))
  "per_connection_buffer_limit_bytes" 262144
  "filter_chains" (list $chain) | toJson -}}
{{- end -}}

{{- define "metrics.mtls.tcp" -}}
{{- $filter := dict "name" "envoy.filters.network.tcp_proxy" "typed_config" (dict
  "@type" "type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy"
  "stat_prefix" .name "cluster" .cluster "idle_timeout" "300s") -}}
{{- include "metrics.mtls.listener" (dict "name" .name "host" .host "port" .port "identities" .identities "filters" (list $filter)) -}}
{{- end -}}

{{- define "metrics.mtls.http" -}}
{{- $policies := dict -}}
{{- $identities := list -}}
{{- range .policies -}}
{{- $identities = append $identities .uri -}}
{{- $method := dict "header" (dict "name" ":method" "string_match" (dict "safe_regex" (dict "google_re2" (dict) "regex" .method))) -}}
{{- $path := dict "url_path" (dict "path" (dict "safe_regex" (dict "google_re2" (dict) "regex" .path))) -}}
{{- $principal := dict "authenticated" (dict "principal_name" (dict "exact" .uri)) -}}
{{- $_ := set $policies .name (dict "permissions" (list (dict "and_rules" (dict "rules" (list $method $path)))) "principals" (list $principal)) -}}
{{- end -}}
{{- $rbac := dict "name" "envoy.filters.http.rbac" "typed_config" (dict
  "@type" "type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBAC"
  "rules" (dict "action" "ALLOW" "policies" $policies)) -}}
{{- $router := dict "name" "envoy.filters.http.router" "typed_config" (dict "@type" "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router") -}}
{{- $route := dict "match" (dict "prefix" "/") "route" (dict "cluster" .cluster "timeout" "60s") -}}
{{- $manager := dict "@type" "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"
  "stat_prefix" .name "normalize_path" true "path_with_escaped_slashes_action" "REJECT_REQUEST"
  "stream_idle_timeout" "60s" "request_headers_timeout" "10s" "max_request_headers_kb" 16
  "route_config" (dict "name" .name "virtual_hosts" (list (dict "name" .name "domains" (list "*") "routes" (list $route))))
  "http_filters" (list $rbac $router) -}}
{{- $filter := dict "name" "envoy.filters.network.http_connection_manager" "typed_config" $manager -}}
{{- include "metrics.mtls.listener" (dict "name" .name "host" "__POD_IP__" "port" .port "identities" (uniq $identities) "filters" (list $filter)) -}}
{{- end -}}

{{- define "metrics.mtls.healthFilter" -}}
{{- $percentages := dict -}}
{{- range .clusters -}}
{{- $_ := set $percentages . (dict "value" 100) -}}
{{- end -}}
{{- dict "name" "envoy.filters.http.health_check" "typed_config" (dict
  "@type" "type.googleapis.com/envoy.extensions.filters.http.health_check.v3.HealthCheck"
  "pass_through_mode" false "cluster_min_healthy_percentages" $percentages
  "headers" (list (dict "name" ":path" "string_match" (dict "exact" .path)))) | toJson -}}
{{- end -}}

{{- define "metrics.mtls.health" -}}
{{- $filters := list
  (include "metrics.mtls.healthFilter" (dict "path" "/healthz" "clusters" .clusters) | fromJson)
  (include "metrics.mtls.healthFilter" (dict "path" "/app-healthz" "clusters" (list "local_health")) | fromJson) -}}
{{- if has "local_reloader_health" .clusters -}}
{{- $filters = append $filters (include "metrics.mtls.healthFilter" (dict "path" "/reloader-healthz" "clusters" (list "local_reloader_health")) | fromJson) -}}
{{- end -}}
{{- $router := dict "name" "envoy.filters.http.router" "typed_config" (dict "@type" "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router") -}}
{{- $route := dict "match" (dict "prefix" "/") "direct_response" (dict "status" 404) -}}
{{- $live := dict "match" (dict "path" "/livez") "direct_response" (dict "status" 200) -}}
{{- $manager := dict "@type" "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"
  "stat_prefix" "health_only" "request_headers_timeout" "3s" "max_request_headers_kb" 4
  "route_config" (dict "name" "health_only" "virtual_hosts" (list (dict "name" "health_only" "domains" (list "*") "routes" (list $live $route))))
  "http_filters" (append $filters $router) -}}
{{- include "metrics.mtls.listener" (dict "name" "health_only" "host" "__POD_IP__" "port" 18081
  "filters" (list (dict "name" "envoy.filters.network.http_connection_manager" "typed_config" $manager))) -}}
{{- end -}}
