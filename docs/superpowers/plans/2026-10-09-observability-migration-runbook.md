# Observability namespace migration runbook

Date: 2026-10-09.

This note is a minimal executable design for renaming the additive observability stack from the first `observability-*` namespaces to normal component namespaces while preserving the current self/canary state. It is intentionally scoped to the new observability stack. It does not change the old Prometheus, Alloy, Loki, Portal, RayTrain backend, or any training workload.

## Current evidence

The 2026-10-09 read-only baseline is stored on the builder at:

```text
/root/raytrain-observability-certmanager-20261009T014000Z
```

It contains `controllers-before.json`, `helm-before.json`, `nodes-before.json`, `pods-before.json`, `pvc-before.json`, and `ray-before.json`. The previous additive rollout evidence remains at:

```text
/root/raytrain-observability-additive-20261008T065709Z
```

The 2026-10-09 baseline shows these old Helm releases:

| Namespace | Release | Status |
| --- | --- | --- |
| `observability-metrics` | `raytrain-vm-operator` | deployed |
| `observability-metrics` | `raytrain-next` | deployed |
| `observability-alerting` | `n9e-cnpg` | deployed |
| `observability-alerting` | `n9e-redis` | deployed |

The same baseline shows 21 PVCs in the old namespaces:

| Namespace | PVCs |
| --- | --- |
| `observability-metrics` | 8: two VMAgent queues, three Alertmanager volumes, three VMStorage volumes |
| `observability-analytics` | 5: two ClickHouse data volumes, three Keeper data volumes |
| `observability-otel` | 2: two OTel queue volumes |
| `observability-alerting` | 6: three Redis volumes, three PostgreSQL volumes |

The old namespaces are still only self-monitoring and synthetic canary scope. Business metrics, real application logs/traces, real notification channels, SSO, and historical migration have not been handed over to this stack. That makes a green deployment into new namespaces safer than rebinding existing PVs.

## Target names

Use lowercase Kubernetes namespace names:

| Old namespace | Target namespace | Main releases |
| --- | --- | --- |
| `observability-metrics` | `victoriametrics` | `victoriametrics-operator`, `victoriametrics-cluster` |
| `observability-analytics` | `clickhouse` | `clickhouse-operator`, `clickhouse-telemetry` |
| `observability-otel` | `opentelemetry` | `opentelemetry-gateway` |
| `observability-alerting` | `nightingale` | `nightingale`, `nightingale-postgres-operator`, `nightingale-redis` |

Do not use uppercase namespace spelling. Avoid names such as `n9e-cnpg` for releases because they hide the real component. The operator can still manage the `nightingale` PostgreSQL cluster, but the release name should say `postgres` or `cloudnative-pg`.

## Migration rule

Do not rename namespaces in place. Kubernetes does not support namespace rename, and the existing PVC claim identity includes namespace/name. Do not make PV rebinding the normal path.

Use this rule for the first migration:

1. Deploy the renamed stack into new namespaces with the same component versions and fixed image digests.
2. Back up the old PostgreSQL configuration database and export the current canary datasets.
3. Restore only the required Nightingale configuration and canary history into the new stack.
4. Keep the old stack scaled down and keep its Retain PVs for a time-boxed recovery window.
5. Delete old PVC/PV/cloud disks only after the window and after the new stack has passed the same live checks.

PV rebinding is an emergency recovery path only. It needs a separate review because StatefulSet-generated PVC names, PV `claimRef`, CNPG metadata, ClickHouse/Keeper generated host names, certificate DNS SANs, and Helm ownership labels all depend on the old namespace and object names.

## Certificate ownership

Move new certificates to cert-manager, but keep the existing mTLS semantics:

- exact URI SANs remain the authorization identity;
- DNS SANs must match the new service names;
- different roles keep separate leaf certificates;
- wrong role from the same CA must still be rejected;
- PostgreSQL, Redis, Alertmanager gossip, and mesh trust domains stay separate unless explicitly redesigned.

The current candidate used `mtls/credentials.py` with a builder-held CA and 90-day leaves. In the renamed stack, use cert-manager `Issuer` or `ClusterIssuer` resources with `Certificate` objects for each role. The cert-manager change must replace only certificate issuance and renewal. It must not weaken Envoy RBAC, SQL authorization, CNPG status authentication, or Redis AUTH/TLS.

If a CA must be trusted across namespaces, publish only the public CA bundle. Do not copy CA private keys into runtime Pods. If `trust-manager` is not installed, use a reviewed public CA Secret/ConfigMap projection per namespace.

## Helm ownership model

Use Helm for release ownership, but keep cluster-scoped resources explicit:

| Group | Helm boundary |
| --- | --- |
| Prerequisites | one `observability-prerequisites` release or reviewed static apply for namespaces, ResourceQuota, PriorityClass, StorageClass, and cert-manager issuers |
| VictoriaMetrics | two releases: operator, then VMCluster/resources |
| ClickHouse | operator release plus a chart for ClickHouse/Keeper CRs, schema ConfigMaps, Services, and bootstrap Job |
| OpenTelemetry | chart for the gateway StatefulSet, ConfigMaps, Services, PDB, and queue PVC templates |
| Nightingale | chart for CNPG operator post-rendered resources, PostgreSQL CR, Redis, bootstrap Job, Nightingale Deployment/Service/PDB |

Cluster-scoped CRDs and webhooks need explicit ownership review before Helm install or upgrade:

- VictoriaMetrics CRDs;
- Altinity ClickHouse and Keeper CRDs;
- CloudNativePG CRDs;
- CNPG webhook configurations;
- ClusterRoles and ClusterRoleBindings emitted by upstream charts.

The old CNPG post-renderer fixed webhook scope to the old namespace. For the new namespace it must be regenerated and revalidated so webhook `namespaceSelector` and service namespace point only to `nightingale`. Do not install a stock CNPG chart without the fixed post-renderer and derived image checks.

## Backup and restore sequence

Run all commands from the approved builder and record output paths in a new private evidence directory. The commands below describe the order and object scope; they should be converted to the exact command set only after the renamed manifests have rendered cleanly.

1. Save a fresh read-only baseline:
   - Helm release list and values for old releases.
   - Controllers, Pods, PVCs, PV reclaim policy, nodes, Ray resources.
   - New namespace absence checks.
   - CRD and webhook owner checks.

2. Freeze old canary input:
   - Stop new synthetic canary producers first.
   - Let OTel queues drain.
   - Confirm VM, ClickHouse, OTel, Nightingale, PostgreSQL, and Redis are Ready before backup.

3. Back up Nightingale PostgreSQL:
   - Use a short-lived Pod or `kubectl exec` path with the existing CNPG-supported credentials.
   - Dump only the `n9e` database needed by Nightingale.
   - Store the dump under the private evidence directory with mode `0600`.
   - Verify restore into an isolated temporary PostgreSQL container or disposable new database before treating the backup as usable.
   - Do not print database passwords, connection strings, or dump contents.

4. Export canary data:
   - Export VM canary series by tenant and time window, or snapshot the VMStorage data only if the restore method has been tested for the same version.
   - Export ClickHouse `observability_canary` tables with schema and data.
   - Record row counts, min/max timestamps, trace/log run IDs, and checksums.
   - OTel queue PVCs should be empty before cutover; if not empty, do not delete or reuse them.

5. Deploy renamed prerequisites:
   - `victoriametrics`, `clickhouse`, `opentelemetry`, `nightingale`.
   - Same non-preempting PriorityClass and Retain StorageClass, unless the existing names are intentionally reused.
   - Quotas with GPU request quota `0`.
   - cert-manager issuers and certificates.

6. Deploy components in dependency order:
   - cert-manager certificates ready.
   - VictoriaMetrics operator, then VM resources.
   - ClickHouse operator, Keeper, ClickHouse, schema bootstrap.
   - OpenTelemetry gateway after ClickHouse schema is ready.
   - CNPG operator, PostgreSQL, Redis, Nightingale bootstrap, Nightingale HA patch.

7. Restore:
   - Restore the Nightingale dump into the new PostgreSQL cluster before starting the final two-replica Nightingale deployment.
   - Restore canary VM/ClickHouse data only if the chosen export/import path was verified.
   - If canary history is not important enough to restore in-band, archive it with the old evidence and start the new stack with fresh canary data.

8. Validate:
   - Repeat the previous live checks for mTLS positive and negative paths.
   - Verify direct Pod-IP native ports are rejected or loopback-only as before.
   - Verify CNPG webhook source checks and real API server admission.
   - Verify Nightingale login, data source query through VMAuth, Redis Sentinel, PostgreSQL TLS/client cert, and cross-Pod behavior.
   - Verify OTel synthetic logs/traces arrive in both ClickHouse replicas with expected IDs and no queue backlog.
   - Verify old RayTrain platform health and active RayJob/RayCluster UIDs are unchanged.

## Cutover and cleanup window

Because no business traffic is attached yet, the first cutover can be configuration-only:

1. Point only the synthetic canary and self-monitoring references to the new namespaces.
2. Keep the old `observability-*` stack running but idle for 24 hours.
3. If the new stack is stable, scale old Deployments/StatefulSets to zero in dependency order:
   - OTel gateway.
   - Nightingale.
   - Redis.
   - PostgreSQL after a final verified backup.
   - VMAlert/Alertmanager/VMAgent/VMAuth/VMSelect/VMInsert/VMStorage.
   - ClickHouse and Keeper.
   - Operators last.
4. Keep old PVCs and PVs for 7 days by default because the StorageClass is Retain and these are the only rollback data source.
5. During the retention window, do not delete old Secrets or PVs. They are sensitive recovery material; restrict access and avoid printing their content.
6. After the window:
   - confirm no Services, Endpoints, Pods, Jobs, and Helm releases remain in old namespaces;
   - archive final inventory and backup verification result;
   - delete old PVCs only with explicit approval;
   - delete released Retain PVs/cloud disks only after confirming the new stack has independent backups.

If the goal is only cleaner names before business onboarding, prefer skipping bulk historical restore for VM/ClickHouse and preserve the old canary exports offline. Preserve the Nightingale PostgreSQL backup because it contains UI/data source/bootstrap configuration that is harder to reconstruct reliably.

## Tests before any live write

The renamed candidate should pass these checks before applying:

- unit tests under `ops/observability/next/tests`;
- Helm template/lint for every release;
- `verify_scope.py` updated for the new namespace allowlist;
- cert-manager Certificate render checks for SAN, URI, EKU, issuer, secret name, and renewal window;
- CNPG post-renderer tests with webhook namespace `nightingale`;
- ClickHouse route and SAN contract tests for `clickhouse` namespace service names;
- OTel config validation with new ClickHouse DNS;
- Nightingale config render tests with new PostgreSQL, Redis, and VMAuth DNS;
- server-side dry-run with CRDs present;
- read-only before/after diff plan showing no old platform, training, MLflow, Loki, Alloy, or Ray resources are changed.

## Open decisions

- Whether to install `trust-manager` for public CA bundle distribution or keep reviewed public CA projections.
- Whether cert-manager issuers are namespaced per component or a cluster issuer with strict approver policy.
- Whether VM and ClickHouse canary history must be restored online, or archived from the old stack and restarted fresh.
- Exact retention window for old Retain PVs and cloud disks. The default in this runbook is 7 days.
- Whether all custom raw YAML should become component Helm charts now, or whether only ownership-critical resources are moved first.
