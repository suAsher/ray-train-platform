# OTel Contrib canary gateway

Status: candidate, not deployment evidence. These files create only a new two-replica StatefulSet and internal services in `observability-otel`; there is no DaemonSet, Kubernetes discovery, host mount, existing Alloy config change or backend rollout.

The Collector binds OTLP, metrics and health to `127.0.0.1`. A pinned, unprivileged Envoy sidecar binds only the downward-API Pod IP. The sidecar requires a valid chain from the new observability mesh CA **and an exact URI SAN for the permitted role**. CA membership alone grants no ingress role. The cluster currently does not enforce Cilium NetworkPolicy; the included policy expresses desired future defense in depth and is not part of this security guarantee. No CNI or existing workload configuration change is required.

| Pod IP port | Permitted identity / operation | Loopback target |
| --- | --- | --- |
| 4317 | `spiffe://observability.raytrain.local/observability-otel/canary`, OTLP gRPC | `127.0.0.1:4317` |
| 4318 | Same canary identity; `POST /v1/logs` and `POST /v1/traces` only | `127.0.0.1:4318` |
| 8888 | `spiffe://observability.raytrain.local/observability-metrics/vmagent`, `GET /metrics` only | `127.0.0.1:8888` |
| 18080 | Kubelet status only: `GET /readyz` or `GET /livez`, 200/503 | Active check of `127.0.0.1:13133/`; no response forwarding |

All other paths on the metrics/HTTP/status listeners return 404. Envoy has no admin listener, host network, host mount, packet redirection or elevated capability. The unauthenticated status listener exposes no Collector health response, metrics, configuration or telemetry data. Its liveness check depends on the local Collector process, not ClickHouse availability; an export outage should queue and retry rather than restart a healthy receiver. The native loopback sockets are still accessible to a compromised container within this same Pod, which is the security boundary.

Required new Secret references:

- `obs-mtls-otel-gateway`: `tls.crt`, `tls.key`, `ca.crt`, mounted read-only at `/etc/obs-mtls` in the gateway and Envoy. The certificate has the gateway URI SAN, both server/client EKUs and the exact service/replica DNS SANs listed in `mtls-identities.json`.
- `otel-clickhouse-auth`: `password`, matching the ClickHouse ingest account.
- The temporary canary producer separately mounts `obs-mtls-canary` (`tls.crt`, `tls.key`, `ca.crt`) with the canary URI SAN. Its key must never be mounted in the gateway, vmagent or a real training workload.
- The CA private key never enters a runtime Pod. Existing observability candidate TLS Secrets are not overwritten or reused by this retrofit.

`mtls-identities.json` is input to the release team's central credential generator. The gateway's ClickHouse exporter now presents its gateway certificate and verifies the ClickHouse service DNS/CA on port 9440; ClickHouse also authenticates the existing `otel_ingest` SQL password and restricts grants. The gateway cannot use SQL-admin or Keeper/Raft channels merely because these certificates share a CA.

`collector.yaml` locks Contrib `0.162.0`, exporter schema creation off, Map columns, TLS server verification on, and authenticated canary data forced to `service.name=observability-canary`. Input resource attributes are cleared, only explicit canary record/span attributes survive, and trusted cluster/source values are inserted before queuing. This is deliberately unsuitable for real application traffic until enrichment and privacy contracts are implemented.

The two 20Gi queue volumes are independent and retained. `file_storage` fsync is enabled and each signal queue is bounded by serialized bytes. Batching occurs **inside** the persistent sending queue, with no preceding asynchronous batch processor. The receiver can acknowledge once durable enqueue succeeds; this is not a promise that ClickHouse or its second replica has committed the data. Requests rejected on queue overflow must be retried by the producer; monitor `enqueue_failed` as well as export failures. A service load balancer does not replicate the two queues. Do not delete a queue PVC with pending data.

On the builder, validate the exact Collector image with `otelcol-contrib validate --config=...` and the pinned Envoy with `envoy --mode validate`, using generated test certificates and credentials, never production secret output. Substitute `__POD_IP__` with the actual isolated test IP before Envoy validation. Then verify the rendered StatefulSet and run live tests of service **and direct Pod IP** access: absent/untrusted/wrong-role certificates fail, canary succeeds on OTLP, vmagent succeeds only on metrics, plaintext fails on data ports, and native port 13133 is unreachable from a different Pod. Probe port 18080 must return only status for the two allowed GET paths, 404 for `/`, `/metrics`, `/config_dump` and OTLP paths, and 503 when the local Collector health check fails. These are release gates; static YAML inspection is not evidence that the runtime matrix passed.

Validate both logs and traces, trace lookup MV replication, exporter retry after a **new canary-only** endpoint interruption, and retained queue recovery. No real service restart or old receiver interruption is required. ConfigMap names are stable for deterministic StatefulSet references, so configuration/certificate updates require an explicit controlled rollout of this new gateway. Plain static Envoy certificate files are not assumed to hot-reload; rotate through a planned new-gateway rollout with trust overlap and verify queue retention. Do not delete queue PVCs to force a rollout.

The tiny trial limits are not throughput claims. Each proxy adds a 100m CPU / 128Mi memory request and 250m CPU / 256Mi limit, without another volume claim. Collect accepted/sent/failed counts, queue bytes, heap, CPU, ClickHouse insert latency and replica lag before enabling a real source. No sampling or metrics ingestion to ClickHouse is configured in this phase. Exact-image configuration and live mTLS tests remain pending until recorded by the release owner.

References: [persistent exporter queues](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/exporter/exporterhelper/README.md), [file storage](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/extension/storage/filestorage/README.md), [ClickHouse exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/exporter/clickhouseexporter/README.md).
