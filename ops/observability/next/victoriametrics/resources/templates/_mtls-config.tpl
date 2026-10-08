{{- define "metrics.mtls.config" -}}
{{- $root := .root -}}
{{- $role := .role -}}
{{- $ns := $root.Release.Namespace -}}
{{- $ports := dict "vmstorage" 8482 "vminsert" 8480 "vmselect" 8481 "vmauth" 8427 "vmagent" 8429 "vmalert" 8880 "alertmanager" 9093 "operator" 8080 -}}
{{- $port := index $ports $role -}}
{{- $uris := dict -}}
{{- range $name := list "vmstorage" "vminsert" "vmselect" "vmauth" "vmagent" "vmalert" "alertmanager" "operator" "canary" "admin" -}}
{{- $_ := set $uris $name (include "metrics.mtls.uri" (dict "namespace" $ns "role" $name)) -}}
{{- end -}}
{{- $clusters := list (include "metrics.mtls.cluster" (dict "name" "local_http" "host" "127.0.0.1" "port" $port) | fromJson) -}}
{{- $healthPort := $port -}}
{{- $healthPath := "/health" -}}
{{- if eq $role "vmauth" -}}{{- $healthPort = 8428 -}}{{- end -}}
{{- if eq $role "alertmanager" -}}{{- $healthPath = "/-/ready" -}}{{- end -}}
{{- if eq $role "operator" -}}{{- $healthPort = 8081 -}}{{- $healthPath = "/readyz" -}}{{- end -}}
{{- $clusters = append $clusters (include "metrics.mtls.cluster" (dict "name" "local_health" "host" "127.0.0.1" "port" $healthPort "health" $healthPath) | fromJson) -}}
{{- $healthClusters := list "local_health" -}}
{{- if has $role (list "vmauth" "vmagent" "vmalert" "alertmanager") -}}
{{- $clusters = append $clusters (include "metrics.mtls.cluster" (dict "name" "local_reloader_health" "host" "127.0.0.1" "port" 8435 "health" "/health") | fromJson) -}}
{{- $healthClusters = append $healthClusters "local_reloader_health" -}}
{{- end -}}
{{- $policies := list (dict "name" "self_metrics" "uri" (index $uris "vmagent") "method" "GET" "path" "/metrics") -}}
{{- $readPath := "/api/v1/(query|query_range|labels|label/[^/]+/values|series|metadata)" -}}
{{- if eq $role "vminsert" -}}
{{- $policies = append $policies (dict "name" "gateway_write" "uri" (index $uris "vmauth") "method" "POST" "path" (printf "/insert/%s/prometheus/api/v1/write" (toString $root.Values.tenantID))) -}}
{{- end -}}
{{- if eq $role "vmselect" -}}
{{- $policies = append $policies (dict "name" "gateway_read" "uri" (index $uris "vmauth") "method" "GET|POST" "path" (printf "/select/%s/prometheus%s" (toString $root.Values.tenantID) $readPath)) -}}
{{- end -}}
{{- if eq $role "vmauth" -}}
{{- $policies = list -}}
{{- range $name := list "vmagent" "vmalert" "canary" "admin" -}}
{{- $policies = append $policies (dict "name" (printf "%s_write" $name) "uri" (index $uris $name) "method" "POST" "path" "/api/v1/write") -}}
{{- end -}}
{{- range $name := list "vmalert" "canary" "admin" -}}
{{- $policies = append $policies (dict "name" (printf "%s_read" $name) "uri" (index $uris $name) "method" "GET|POST" "path" $readPath) -}}
{{- end -}}
{{- $policies = append $policies (dict "name" "n9e_read" "uri" $root.Values.mtls.readRoleURI "method" "GET|POST" "path" $readPath) -}}
{{- end -}}
{{- if eq $role "alertmanager" -}}
{{- $policies = append $policies (dict "name" "send_canary_alerts" "uri" (index $uris "vmalert") "method" "POST" "path" "/api/v2/alerts") -}}
{{- $policies = append $policies (dict "name" "admin_read" "uri" (index $uris "admin") "method" "GET" "path" "/api/v2/(status|alerts|silences)") -}}
{{- $policies = append $policies (dict "name" "admin_test_silence" "uri" (index $uris "admin") "method" "POST" "path" "/api/v2/silences") -}}
{{- end -}}
{{- $listeners := list (include "metrics.mtls.http" (dict "name" "http_ingress" "port" $port "cluster" "local_http" "policies" $policies) | fromJson) -}}
{{- if eq $role "vmauth" -}}
{{- $clusters = append $clusters (include "metrics.mtls.cluster" (dict "name" "local_internal" "host" "127.0.0.1" "port" 8428) | fromJson) -}}
{{- $listeners = append $listeners (include "metrics.mtls.http" (dict "name" "internal_metrics" "port" 8428 "cluster" "local_internal"
  "policies" (list (dict "name" "self_metrics" "uri" (index $uris "vmagent") "method" "GET" "path" "/metrics"))) | fromJson) -}}
{{- range $destination := list (dict "role" "vminsert" "localPort" 18480 "port" 8480) (dict "role" "vmselect" "localPort" 18481 "port" 8481) -}}
{{- $name := printf "egress_%s" $destination.role -}}
{{- $host := printf "%s-%s.%s.svc" $destination.role $root.Values.name $ns -}}
{{- $clusters = append $clusters (include "metrics.mtls.cluster" (dict "name" $name "host" $host "port" $destination.port "peer" (index $uris $destination.role)) | fromJson) -}}
{{- $listeners = append $listeners (include "metrics.mtls.tcp" (dict "name" $name "host" "127.0.0.1" "port" $destination.localPort "cluster" $name) | fromJson) -}}
{{- end -}}
{{- end -}}
{{- if eq $role "vmstorage" -}}
{{- range $rpc := list (dict "port" 8400 "native" 28400 "role" "vminsert") (dict "port" 8401 "native" 28401 "role" "vmselect") -}}
{{- $name := printf "rpc_%s" $rpc.role -}}
{{- $clusters = append $clusters (include "metrics.mtls.cluster" (dict "name" $name "host" "127.0.0.1" "port" $rpc.native) | fromJson) -}}
{{- $listeners = append $listeners (include "metrics.mtls.tcp" (dict "name" $name "host" "__POD_IP__" "port" $rpc.port "cluster" $name "identities" (list (index $uris $rpc.role))) | fromJson) -}}
{{- end -}}
{{- end -}}
{{- if has $role (list "vminsert" "vmselect") -}}
{{- $base := 18400 -}}{{- $remote := 8400 -}}
{{- if eq $role "vmselect" -}}{{- $base = 19400 -}}{{- $remote = 8401 -}}{{- end -}}
{{- range $index := until (int $root.Values.storage.replicas) -}}
{{- $name := printf "storage_%d" $index -}}
{{- $host := printf "vmstorage-%s-%d.vmstorage-%s.%s.svc" $root.Values.name $index $root.Values.name $ns -}}
{{- $clusters = append $clusters (include "metrics.mtls.cluster" (dict "name" $name "host" $host "port" $remote "peer" (index $uris "vmstorage")) | fromJson) -}}
{{- $listeners = append $listeners (include "metrics.mtls.tcp" (dict "name" $name "host" "127.0.0.1" "port" (add $base $index) "cluster" $name) | fromJson) -}}
{{- end -}}
{{- end -}}
{{- $listeners = append $listeners (include "metrics.mtls.health" (dict "clusters" $healthClusters) | fromJson) -}}
{{/* No HTTP access_log: Authorization headers and query strings must not reach logs. */}}
{{- dict "static_resources" (dict "listeners" $listeners "clusters" $clusters)
  "overload_manager" (dict "resource_monitors" (list (dict
    "name" "envoy.resource_monitors.global_downstream_max_connections"
    "typed_config" (dict "@type" "type.googleapis.com/envoy.extensions.resource_monitors.downstream_connections.v3.DownstreamConnectionsConfig" "max_active_downstream_connections" 512)))) | toJson -}}
{{- end -}}
