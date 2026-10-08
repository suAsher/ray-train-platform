# OTel Contrib canary gateway

Status: candidate, not deployment evidence. These files create only a new two-replica StatefulSet and internal services in `observability-otel`; there is no DaemonSet, Kubernetes discovery, host mount, existing Alloy config change or backend rollout.

Both receivers require certificates signed by a **dedicated canary-only CA**. The release process must mint only the synthetic canary identity from this CA. NetworkPolicy further limits ingress to Pods in this namespace with `observability.raytrain.io/component=otel-canary`. User workloads, SDKs and Alloy have neither network admission nor this credential. Do not share this CA with production identities later; introduce reviewed separate pipelines and identities first.

Required new Secret references:

- `otel-canary-server-tls`: `tls.crt`, `tls.key`, `ca.crt`. `ca.crt` is the client CA; cert SAN includes `otel-gateway.observability-otel.svc` and `.svc.cluster.local`, plus pod DNS names if testing direct replicas.
- `otel-clickhouse-auth`: `password`, matching the ClickHouse ingest account.
- `clickhouse-server-ca`: `ca.crt`, matching the ClickHouse server cert.
- The temporary canary producer separately mounts `otel-canary-client-tls` (`tls.crt`, `tls.key`, `ca.crt`), where the CA verifies the gateway's server certificate. The CA private key never enters a runtime pod.

`collector.yaml` locks Contrib `0.162.0`, exporter schema creation off, Map columns, TLS server verification on, and authenticated canary data forced to `service.name=observability-canary`. Input resource attributes are cleared, only explicit canary record/span attributes survive, and trusted cluster/source values are inserted before queuing. This is deliberately unsuitable for real application traffic until enrichment and privacy contracts are implemented.

The two 20Gi queue volumes are independent and retained. `file_storage` fsync is enabled and each signal queue is bounded by serialized bytes. Batching occurs **inside** the persistent sending queue, with no preceding asynchronous batch processor. The receiver can acknowledge once durable enqueue succeeds; this is not a promise that ClickHouse or its second replica has committed the data. Requests rejected on queue overflow must be retried by the producer; monitor `enqueue_failed` as well as export failures. A service load balancer does not replicate the two queues. Do not delete a queue PVC with pending data.

On the builder, validate the exact image with `otelcol-contrib validate --config=...` using generated test certificates and credentials, never production secret output. Then verify the rendered StatefulSet, TLS negative tests, target schema and NetworkPolicy before the low-rate synthetic run. Validate both logs and traces, trace lookup MV replication, exporter retry after a **new canary-only** endpoint interruption, and retained queue recovery. No real service restart or old receiver interruption is required.

The tiny trial limits are not throughput claims. Collect accepted/sent/failed counts, queue bytes, heap, CPU, ClickHouse insert latency and replica lag before enabling a real source. No sampling or metrics ingestion to ClickHouse is configured in this phase.

References: [persistent exporter queues](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/exporter/exporterhelper/README.md), [file storage](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/extension/storage/filestorage/README.md), [ClickHouse exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/exporter/clickhouseexporter/README.md).
