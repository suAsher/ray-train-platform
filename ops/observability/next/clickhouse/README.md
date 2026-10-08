# ClickHouse / Keeper additive candidate

Status: candidate files only. Production apply, image digests and acceptance are recorded by the parent release run. Do not interpret this directory as deployment evidence.

This installs a fresh, internal-only observability database in `observability-analytics`. It never mounts old Loki/Prometheus/MLflow or user storage. It imports no historical data, and the initial database is `observability_canary` with seven-day TTLs. The two ClickHouse replicas and three Keeper peers must span nodes; CPU/memory requests and limits are bounded, GPU resources are absent, and the shared priority class is non-preempting. PVCs and the new StorageClass use Retain.

## Locked upstream candidates

| Component | Candidate | Source |
| --- | --- | --- |
| Altinity Operator chart/controller | `0.27.4` / `release-0.27.4` | [chart](https://github.com/Altinity/clickhouse-operator/tree/release-0.27.4/deploy/helm/clickhouse-operator) |
| ClickHouse server / Keeper | `26.8.20.9` LTS | [release](https://github.com/ClickHouse/ClickHouse/releases/tag/v26.8.20.9-lts) |
| Exporter schema | Collector Contrib `0.162.0` | [templates](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.162.0/exporter/clickhouseexporter/internal/sqltemplates) |

The builder must resolve approved pull locations and immutable image digests before applying. Candidate tags are exact versions, not proof of a successful pull or a tested combination.

## Cluster-scoped footprint

The chart installs **no CRD hook**. Review and install only these pinned-version CRDs, after proving no existing objects/controller ownership conflict:

- `clickhouseinstallations.clickhouse.altinity.com`
- `clickhouseinstallationtemplates.clickhouse.altinity.com`
- `clickhouseoperatorconfigurations.clickhouse.altinity.com`
- `clickhousekeeperinstallations.clickhouse-keeper.altinity.com`

They are available in `deploy/helm/clickhouse-operator/crds/` of the locked release. Never apply from `master`, overwrite an existing different CRD, or remove a CRD during rollback. The controller's chart RBAC is namespace scoped and its watch expression is anchored to `observability-analytics`. Rendered RBAC and runtime permission errors must be checked; a namespaced Role cannot grant access to cluster-scoped PVs. Do not remedy errors with blanket cluster-admin. If direct PV reads are required, review an explicit minimal read-only grant separately; existing PVs must never be patched/deleted by this controller.

## Credential contract

Only names and keys are committed. Generate random credentials in the release workspace; do not print values, put them in arguments, or serialize full Secrets into artifacts.

| Namespace | Secret | Keys / purpose |
| --- | --- | --- |
| analytics | `clickhouse-operator-auth` | `username=clickhouse_operator`, `password`; operator-created restricted network identity |
| analytics | `clickhouse-auth` | `default-password-sha256`, `admin-password-sha256`, `ingest-password-sha256`, `query-password-sha256` |
| analytics | `clickhouse-bootstrap-auth` | `password`; plaintext corresponding to admin hash, bootstrap only |
| analytics | `clickhouse-query-auth` | `password`; plaintext corresponding to query hash, only approved query/canary consumer |
| analytics | `clickhouse-server-tls` | `tls.crt`, `tls.key`, `ca.crt`; native TCP TLS |
| otel | `otel-clickhouse-auth` | `password`; plaintext corresponding to ingest hash |
| otel | `clickhouse-server-ca` | `ca.crt`; CA for server verification |

Here `analytics` means `observability-analytics`; `otel` means `observability-otel`. Server cert SANs must include `clickhouse-observability.observability-analytics.svc` and `.svc.cluster.local`. A query certificate need not be issued: native TLS verifies the server, SQL password verifies the account. OTLP uses a separate canary-only CA with client authentication.

`schema_admin` is only for this fresh cluster's bootstrap; it is not an application credential. XML grants restrict `otel_ingest` to INSERT and DESCRIBE metadata on the two canary input tables. The v0.162.0 exporter probes schema with `DESC TABLE`, so no broad `SELECT system.columns` grant is necessary. `observability_query` is read-only within the canary database. The trace lookup MV explicitly uses its schema definer; the ingest identity receives no DDL or read access. Test these boundaries with the actual image before admitting canary traffic.

## Builder validation and ordered apply

1. Render chart `0.27.4` with `operator-values.yaml`; inspect all resources, image locations, namespace-only RBAC, watch namespace and probes. No ServiceMonitor is created, so old Prometheus cannot adopt extra targets.
2. Render `kubectl kustomize ops/observability/next/clickhouse` and server-side dry-run with the pinned CRDs. Inspect generated CH/CHK template fields, storage retention, readiness endpoints and resource requests. Verify the existing CNI actually enforces the NetworkPolicies.
3. Apply new namespace/StorageClass/priority prerequisites, secrets, reviewed CRDs, new operator, then the ClickHouse kustomization. Apply does not create the schema Job automatically.
4. Wait for all three Keeper peers and both ClickHouse replicas; inspect imageID, PVC bindings, each node and the Raft leader/followers. No existing workload may be evicted or restarted.
5. Apply `bootstrap-job.yaml` only after both replicas are Ready. `CLICKHOUSE_PASSWORD` comes from a Secret environment reference and is not supplied in argv. The Job executes only the versioned `schema.sql` and `ON CLUSTER telemetry`. It does not insert real data or mutate old databases.
6. Verify the schema on **both replicas**, including three ReplicatedMergeTree tables, all distributed query tables and the trace lookup MV. Check `system.replicas`, macros and Keeper paths; do not treat Job exit 0 alone as replica acceptance.
7. Only after schema validation start the dedicated canary OTel gateway. Confirm allowed insert, denied SELECT/DDL for the writer and denied INSERT for the query account. Submit synthetic logs/spans; after replication catches up, query solely from replica B, including trace-id lookup and aggregation. Record expected IDs/counts and queue drain.

No `OPTIMIZE FINAL`, wide data scans, failure injection against existing components or history migration is part of this phase. Resources are a small-load trial budget (CH 2 × 1 CPU/4Gi requested, Keeper 3 × 250m/512Mi); they are not measured production capacity. Native protocol 9440 is the only OTEL ingress. Operator and bootstrap plaintext ports are confined by NetworkPolicy to new namespace identities; Keeper and inter-replica replication traffic is confined to new pods.

## Recovery boundary

Stop new ingress or scale only these new canary receivers down after draining. Keep PVCs, CRDs and schema unless separately authorized to delete this phase's test data. A CH node loss can lose not-yet-replicated acknowledged data: `wait_for_async_insert=1` confirms local flush, not quorum replication. Current initial design accepts that telemetry RPO; the old training and log systems remain the authority. Do not claim exactly-once delivery or zero RPO from two replicas.
