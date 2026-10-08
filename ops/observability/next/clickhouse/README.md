# ClickHouse / Keeper additive candidate

Status: candidate files only. Production apply, image digests and acceptance are recorded by the parent release run. Do not interpret this directory as deployment evidence.

This installs a fresh, internal-only observability database in `observability-analytics`. It never mounts old Loki/Prometheus/MLflow or user storage. It imports no historical data, and the initial database is `observability_canary` with seven-day TTLs. The two ClickHouse replicas and three Keeper peers must span nodes; CPU/memory requests and limits are bounded, GPU resources are absent, and the shared priority class is non-preempting. PVCs and the new StorageClass use Retain.

## Locked upstream candidates

| Component | Candidate | Source |
| --- | --- | --- |
| Altinity Operator chart/controller | `0.27.4` / `release-0.27.4` | [chart](https://github.com/Altinity/clickhouse-operator/tree/release-0.27.4/deploy/helm/clickhouse-operator) |
| ClickHouse server / Keeper | `26.8.20.9` LTS | [release](https://github.com/ClickHouse/ClickHouse/releases/tag/v26.8.20.9-lts) |
| Exporter schema | Collector Contrib `0.162.0` | [templates](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.162.0/exporter/clickhouseexporter/internal/sqltemplates) |

Image references now use the existing `harbor.wellspiking.ai/hub` proxy with immutable manifest digests, inspected on the builder on 2026-10-08. The OTel digest also matched the official GHCR manifest, and its offline configuration validation passed. Namespace pull authorization, generated resources and the runtime combination still require deployment acceptance; a valid manifest is not a successful cluster rollout.

## Cluster-scoped footprint

The chart installs **no CRD hook**. Review and install only these pinned-version CRDs, after proving no existing objects/controller ownership conflict:

- `clickhouseinstallations.clickhouse.altinity.com`
- `clickhouseinstallationtemplates.clickhouse.altinity.com`
- `clickhouseoperatorconfigurations.clickhouse.altinity.com`
- `clickhousekeeperinstallations.clickhouse-keeper.altinity.com`

They are available in `deploy/helm/clickhouse-operator/crds/` of the locked release. Never apply from `master`, overwrite an existing different CRD, or remove a CRD during rollback. The controller's chart RBAC is namespace scoped and its watch list contains only the literal `observability-analytics`. Altinity 0.27.4 falls back to cluster-wide informer/cache LISTs when given regex patterns, even an anchored single-namespace expression; the first stopped-resource deployment caught and rejected that case. Rendered RBAC and runtime permission errors must be checked; a namespaced Role cannot grant access to cluster-scoped PVs. Do not remedy errors with blanket cluster-admin. If direct PV reads are required, review an explicit minimal read-only grant separately; existing PVs must never be patched/deleted by this controller.

## Credential contract

Only names and keys are committed. Generate random credentials in the release workspace; do not print values, put them in arguments, or serialize full Secrets into artifacts.

| Namespace | Secret | Keys / purpose |
| --- | --- | --- |
| analytics | `clickhouse-operator-auth` | `username=clickhouse_operator`, `password`; operator-created restricted network identity |
| analytics | `clickhouse-auth` | `default-password-sha256`, `admin-password-sha256`, `ingest-password-sha256`, `query-password-sha256` |
| analytics | `clickhouse-bootstrap-auth` | `password`; plaintext corresponding to admin hash, bootstrap only |
| analytics | `clickhouse-query-auth` | `password`; plaintext corresponding to query hash, only approved query/canary consumer |
| analytics | `obs-mtls-clickhouse`, `obs-mtls-keeper`, `obs-mtls-ch-operator` | new per-role key, certificate and CA (`tls.crt`, `tls.key`, `ca.crt`) |
| analytics | `obs-mtls-schema-admin`, `obs-mtls-query-admin` | separate approved bootstrap/query client identities |
| analytics | `obs-clickhouse-interserver-auth` | `username=replica`, independent random `password`; XML `from_env` only |
| otel | `otel-clickhouse-auth` | `password`; plaintext corresponding to ingest hash |
| otel | `obs-mtls-otel-gateway`, `obs-mtls-canary` | separate gateway/writer test identities; same mesh trust bundle |

Here `analytics` means `observability-analytics`; `otel` means `observability-otel`. `mtls-identities.json` is the complete fixed DNS/EKU contract, consumed by the parent release credential tool. Every client needs its own role certificate plus any applicable SQL password. Envoy validates both the mesh CA and exact URI SAN; merely presenting another certificate from the same CA is insufficient. Old Secrets are retained and are no longer referenced by these workloads. Never mount an admin/client identity into another workload.

## Proxy and native listener contract

The existing CNI is unchanged and does not enforce the candidate NetworkPolicies. The security boundary is therefore native loopback binding plus application mTLS, not labels or Service visibility. Every Envoy binds its Pod IP from the downward API; no wildcard bind, privileged container, host network, iptables or NET_ADMIN is used. No access logs or public Envoy admin endpoint are enabled.

| Pod / port | Allowed URI role | Loopback upstream |
| --- | --- | --- |
| ClickHouse 8443 | analytics/ch-operator, analytics/schema-admin | HTTP 8123 |
| ClickHouse 9440 | otel/otel-gateway, analytics/clickhouse, analytics/schema-admin, analytics/query-admin | native 9000 |
| ClickHouse 9010 | analytics/clickhouse | native interserver HTTPS 9010, with independent replica password |
| ClickHouse 9363 | metrics/vmagent, GET /metrics only | 9363 |
| Keeper 2281 | analytics/clickhouse, analytics/ch-operator | native TLS 2281 |
| Keeper 9234 | analytics/keeper only | native Raft TLS 9234 |
| Keeper 7000 / operator 9999 | metrics/vmagent, GET /metrics only | matching loopback port |
| Each Pod 18080 | unauthenticated GET /readyz or /livez only | status code from active local health check; no backend body |

All URI roles above expand to `spiffe://observability.raytrain.local/observability-<namespace>/<role>`. SQL passwords/grants remain enforced after TLS. Both `listen_host` and `interserver_listen_host` are replaced with `127.0.0.1`; native CH 9000/8123 and Keeper control 9182 cannot be reached via Pod IP. Inspect actual generated config and socket listeners before accepting this invariant.

ClickHouse also retains a strict TLS native listener on **127.0.0.1:9440**, while Envoy exclusively owns **Pod-IP:9440**. ClickHouse's distributed DDL self-identification reads `tcp_port_secure`; removing that native setting while advertising secure replicas on 9440 causes DDL workers to skip their own tasks. The `c360143` full Docker run caught this mismatch. Runtime acceptance now checks each replica has exactly one `system.clusters` local member on port 9440 before executing schema DDL. This does not open a Pod-IP bypass or relax mTLS.

Each of the two fixed replica settings also supplies its exact `interserver_https_host` FQDN from the existing certificate SAN list. Altinity 0.27.4 generates only `interserver_http_host`; [ClickHouse 26.8's server](https://github.com/ClickHouse/ClickHouse/blob/v26.8.20.9-lts/programs/server/Server.cpp#L2140-L2165) reads the HTTPS name separately and otherwise advertises the container hostname. The `00f6f02` run caught this during actual OTel cross-replica fetches after SQL, schema and ingest had passed. The route contract rejects missing, swapped or renamed replica settings rather than allowing unverified hostnames. No certificate validation is disabled.

Keeper Raft retains real generated peer DNS names and native TLS. Only the CH and operator ZooKeeper clients use three fixed loopback egress ports 12181–12183; each points to one secure Keeper client Service. Do not replace Raft advertised members with localhost. Keeper health is checked locally; process liveness does not restart a follower simply because quorum is temporarily absent.

The final Keeper XML explicitly merges `keeper_server.raft_configuration.secure=true`. ClickHouse 26.8 reads this cluster-wide flag; the pinned Altinity generator's per-server `secure` tag alone does not enable native Raft TLS. The isolated `eb77ce1` run caught this mismatch before deployment (election/EOF loop); the overlay leaves generated peer DNS/IDs untouched. Runtime success for the corrected candidate remains a separate required gate.

Keeper's local proxy-to-native TLS contexts set Envoy `max_session_keys: 0`. In the `c741e72` isolated test, NuRaft reported `session id context uninitialized` when resuming a mutually authenticated native TLS session, and the proxy closed a valid downstream Raft handshake. Disabling resumption on these two local hops requires a full certificate-authenticated handshake for each connection; it does not disable certificate, CA or exact-role verification. Re-run the unchanged strict positive/negative matrix and quorum test before accepting the correction.

Altinity 0.27.4's CH HTTP client does not accept a client certificate and does not honor HTTP_PROXY. The scoped `operator-post-render.py` injects two fixed hostAliases, two Envoy egress routes and a new role Secret. `GODEBUG=netdns=go` ensures `/etc/hosts` names and trailing-dot FQDNs normalize identically. Upstream alias Services use different names to prevent recursion. `validate_mtls.py` rejects altered topology, missing aliases, insecure/wrong upstreams and expansion without reviewed routes. Unknown replicas fail closed at the absent Pod-IP HTTP port. Expansion requires updating this contract and runtime acceptance first.

The operator-generated SQL account normally restricts source IP to the operator Pod. The final `users.d/zz-mtls-operator.xml` replaces only that account's network ACL with `127.0.0.1`, because Envoy forwards a role-authenticated connection from loopback. Its generated password and grants remain untouched. Inspect the merged users config and prove a no-certificate/wrong-role client cannot access HTTP before accepting this adjustment.

`schema_admin` is only for this fresh cluster's bootstrap; it is not an application credential. XML grants restrict `otel_ingest` to INSERT and DESCRIBE metadata on the two canary input tables. The v0.162.0 exporter probes schema with `DESC TABLE`, so no broad `SELECT system.columns` grant is necessary. `observability_query` is read-only within the canary database. The trace lookup MV explicitly uses its schema definer; the ingest identity receives no DDL or read access. Test these boundaries with the actual image before admitting canary traffic.

## Builder validation and ordered apply

1. On the builder, run the CH/OTel unittest contracts and `python3 clickhouse/validate_mtls.py`. Render chart `0.27.4` with `operator-values.yaml` **and `--post-renderer <absolute>/clickhouse/operator-post-render.py`**; an unmodified chart is not deployable. Inspect image locations, namespace-only RBAC, watch namespace, hostAliases, metrics bind and probes. No ServiceMonitor is created, so old Prometheus cannot adopt extra targets.
2. Render `kubectl kustomize ops/observability/next/clickhouse` and server-side dry-run with the pinned CRDs. Validate every Envoy configuration with the exact image and temporary test certificates. Inspect generated CH/CHK template fields, port mapping, secure remote_servers, PVC retention and resources. Do not enable or modify CNI policy enforcement.
3. Apply only the new prerequisites, new credentials, reviewed CRDs, proxy/schema ConfigMaps, alias Services, then the post-rendered operator. Apply Keeper first and wait for all three peers. Only then apply CHI. The pinned operator generates one `maxUnavailable: 1` PDB per cluster; do not add overlapping handwritten PDBs. Do not apply the complete kustomization as an unordered first installation; its render is for review. Bootstrap is separate.
4. Wait for all three Keeper peers and both ClickHouse replicas; inspect imageID, PVC bindings, each node and the Raft leader/followers. No existing workload may be evicted or restarted.
5. Apply `bootstrap-job.yaml` only after both replicas are Ready. `CLICKHOUSE_PASSWORD` comes from a Secret environment reference and is not supplied in argv. The Job executes only the versioned `schema.sql` and `ON CLUSTER telemetry`. It does not insert real data or mutate old databases.
6. Verify the schema on **both replicas**, including three ReplicatedMergeTree tables, all distributed query tables and the trace lookup MV. Check `system.replicas`, macros and Keeper paths; do not treat Job exit 0 alone as replica acceptance.
7. Only after schema validation start the dedicated canary OTel gateway. Confirm allowed insert, denied SELECT/DDL for the writer and denied INSERT for the query account. Submit synthetic logs/spans; after replication catches up, query solely from replica B, including trace-id lookup and aggregation. Record expected IDs/counts and queue drain.

8. Negative acceptance must test no certificate, untrusted CA, wrong role on the same CA, plaintext bypass, direct Pod-IP native/admin ports and unknown HTTP paths. Test authorized SQL read/write restrictions separately. Scan generated native listeners, confirm operator reconciles both fixed replicas, and prove replica count expansion is rejected by the route gate.

No `OPTIMIZE FINAL`, wide scans, failure injection against existing components or history migration is part of this phase. Resources remain a small-load trial budget (CH 2 × 1 CPU/4Gi, Keeper 3 × 250m/512Mi requested) plus one Envoy per Pod and one for the operator (each 100m/128Mi requested, 250m/256Mi limit). They are not measured production capacity. These files are candidates, not runtime verification evidence.

Certificate files are mounted as a whole Secret volume. Static Envoy TLS configuration does not automatically reload rotated files: use controlled rollouts of only these new components (Keeper one at a time, CH one at a time) after an overlap trust-bundle validation. Never roll old components or remove their existing credentials as part of this phase.

## Isolated builder runtime gate

After bundling the candidate to the approved Linux builder, run as root:

```sh
python3 ops/observability/next/clickhouse/verify-runtime.py --output /root/<new-private-validation-directory>
```

This requires the already inspected pinned images locally, Docker, OpenSSL and PyYAML. It creates an internal Docker network, publishes no host port, and mounts only disposable local storage and each role's leaf certificate directory. Signing keys are never mounted into containers. Native processes and Envoy use UID 10001, dropped capabilities, read-only roots and bounded CPU/memory. Three Keeper processes and two CH servers exercise candidate XML/proxy configuration, schema DDL, native bypass rejection, same-CA wrong-role rejection, SQL least privileges, one OTel log and one trace, cross-replica data/MV checks and OTel status transitions. It never reads production credentials or invokes Kubernetes.

Cleanup removes only its uniquely named containers/network, signing material and fake data; a redacted `result.json` and private diagnostics remain. Failure is failure, not a skipped success. This Docker test builds the native topology from candidate settings and does not reproduce operator reconciliation: independently inspect actual generated ConfigMaps/Services/probes, both replicas' macros and final socket binds before Kubernetes acceptance. Queue crash/restart recovery and a complete gRPC Export RPC remain separate gates; the test states these omissions explicitly.

The trial explicitly reduces buffer/distributed/message-broker/streaming/common background pools and async-insert workers. Leaving the 26.8 defaults while limiting the global pool to 128 caused an authenticated `SELECT 1` to stall: the isolated reproduction measured 128 active global workers and 146 scheduled jobs, while the container used 154 of 256 PID slots with no PID-limit events. The fix preserves the global, CPU, memory and container PID ceilings; runtime acceptance must still pass real HTTP/native SQL, replicated DDL and OTel ingestion. A successful TLS handshake or `/ping` alone does not establish query readiness.

## Recovery boundary

Stop new ingress or scale only these new canary receivers down after draining. Keep PVCs, CRDs and schema unless separately authorized to delete this phase's test data. A CH node loss can lose not-yet-replicated acknowledged data: `wait_for_async_insert=1` confirms local flush, not quorum replication. Current initial design accepts that telemetry RPO; the old training and log systems remain the authority. Do not claim exactly-once delivery or zero RPO from two replicas.
