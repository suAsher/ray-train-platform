# Observability namespace migration runbook

Date: 2026-10-09.

Execution status: the normal product namespaces are deployed and have passed the new-location protocol checks. The final runtime candidate is `4768871`; the negative-certificate acceptance helper is `741b7f2` (296 unit tests passed on the builder). Controlled renewal of six native roles, serial replacement of 13 Pods, and a second no-op run passed. The periodic rotation CronJob remains suspended while the new stack has no real failure-notification receiver. See the current [deployment record](../../../ops/observability/next/DEPLOYMENT_RUNBOOK.md) for final inventory and evidence. The old additive workloads and all 21 old PVCs remain intact; old retirement has not run.

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

| Old namespace | Target namespace | Actual release / resource names |
| --- | --- | --- |
| `observability-metrics` | `victoriametrics` | `victoriametrics-operator`, `victoriametrics-cluster` |
| `observability-analytics` | `clickhouse` | Helm `clickhouse-operator`; CHI/CHK `clickhouse` |
| `observability-otel` | `opentelemetry` | StatefulSet `otel-gateway`, managed by Kustomize |
| `observability-alerting` | `nightingale` | Helm `cloudnative-pg-operator`, `nightingale-redis`; Cluster `nightingale-postgres`; Deployment `nightingale` |

Do not use uppercase namespace spelling. Avoid names such as `n9e-cnpg` for releases because they hide the real component. The operator can still manage the `nightingale` PostgreSQL cluster, but the release name should say `postgres` or `cloudnative-pg`.

## Migration rule

Do not rename namespaces in place. Kubernetes does not support namespace rename, and the existing PVC claim identity includes namespace/name. Do not make PV rebinding the normal path.

Use this rule for the first migration:

1. Deploy the renamed stack into new namespaces with the same component versions and fixed image digests.
2. Back up the old PostgreSQL configuration database and export the current canary datasets.
3. Restore only the required Nightingale configuration and canary history into the new stack.
4. After a separate retirement review, scale down the old additive stack and keep its Retain PVs for a time-boxed recovery window. This step has not been executed; both stacks currently remain running.
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
| Prerequisites | Reviewed static apply for namespaces, ResourceQuota, PriorityClass, StorageClass, and issuer/policy objects; two Helm releases for cert-manager and approver-policy |
| VictoriaMetrics | Two Helm releases: VMCluster/resources first to provide proxy ConfigMaps, then the Operator |
| ClickHouse | Helm Operator; Kustomize for ClickHouse/Keeper CRs, proxy ConfigMaps and Services; explicit schema Job |
| OpenTelemetry | Kustomize for gateway StatefulSet, ConfigMaps, Services, PDB, and queue PVC templates; no Helm release |
| Nightingale | Helm CNPG Operator and Redis; repository manifests for PostgreSQL CR, bootstrap and Nightingale Deployment/Service/PDB |

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

## 2026-10-09 executable deployment notes (candidate `7c6ed96` and later reviewed fixes)

These notes record read-only findings and proposed commands, not completed deployment. Use the latest builder-tested candidate after the PostgreSQL cert-manager reference fix; do not deploy the `pg-cert-red` worktree just because its source is present. Only the release coordinator executes cluster writes.

The current implementation keeps official Operator/Redis Helm releases and the existing reviewed raw/Kustomize Operator CRs. There is no `clickhouse-telemetry`, `opentelemetry-gateway`, or `nightingale` application Helm chart in this candidate. These can be packaged later; do not invent a Helm release or take existing ownership to answer the management question.

| Resources | Actual manager |
| --- | --- |
| VM Operator and its extra read-only ClusterRole | Helm `victoriametrics-operator` |
| VM CRs, proxy ConfigMaps, namespaced RBAC, Services and policies | Local Helm `victoriametrics-cluster`; VM Operator owns generated workloads |
| ClickHouse Operator | Helm `clickhouse-operator` with scoped post-renderer |
| ClickHouse/Keeper CRs, ConfigMaps, Services, policy | Kustomize/apply; Altinity Operator owns generated workloads |
| OTel gateway | Kustomize/apply |
| CNPG Operator | Helm `cloudnative-pg-operator`, official namespace-suffixed webhooks |
| PostgreSQL Cluster | Apply the canonical `20-postgres.yaml`; CNPG owns generated workloads |
| Redis/Sentinel | Helm `nightingale-redis` |
| Nightingale configuration/application/bootstrap | Reviewed ConfigMap/apply, then the explicit HA patch |

### Verified builder paths and fixed credentials

All four archives already exist at `/root/raytrain-observability-additive-20261008T065709Z/charts/`:

- `victoria-metrics-operator-0.68.1.tgz`
- `altinity-clickhouse-operator-0.27.4.tgz`
- `cloudnative-pg-0.29.1.tgz`
- `redis-ha-4.39.0.tgz`

The local source is `/Users/ashersu/.codex/worktrees/observability-additive/ray-train-platform`. On the builder, `/root/raytrain-observability-certmanager-20261009T014000Z/pg-cert-red` was the read-only `7c6ed96` snapshot. `OBS_CANDIDATE` below must point to the subsequent final GREEN detached worktree, with its SHA verified against the release candidate. Do not point it at `/opt/guofeng/vke-cluster/ray-platform-main` or an unverified copied folder.

Copy only these explicit non-PKI credential pairs through memory/stdin, preserving `type` and `data`; create only after confirming **all** destinations absent. Strip old UID/resourceVersion/ownerReferences/Helm and last-applied annotations. Print only source/destination names. A partial failure must be inspected; do not overwrite a destination.

| Old namespace | Source Secret | New namespace | Destination Secret |
| --- | --- | --- | --- |
| observability-metrics | harbor-registry | victoriametrics | harbor-registry |
| observability-metrics | vm-self-read | victoriametrics | vm-self-read |
| observability-metrics | vm-self-write | victoriametrics | vm-self-write |
| observability-analytics | clickhouse-auth | clickhouse | clickhouse-auth |
| observability-analytics | clickhouse-operator-auth | clickhouse | clickhouse-operator-auth |
| observability-analytics | clickhouse-bootstrap-auth | clickhouse | clickhouse-bootstrap-auth |
| observability-analytics | clickhouse-query-auth | clickhouse | clickhouse-query-auth |
| observability-analytics | obs-clickhouse-interserver-auth | clickhouse | obs-clickhouse-interserver-auth |
| observability-otel | otel-clickhouse-auth | opentelemetry | otel-clickhouse-auth |
| observability-alerting | n9e-db-app | nightingale | n9e-db-app |
| observability-alerting | n9e-bootstrap-admin | nightingale | n9e-bootstrap-admin |
| observability-alerting | n9e-vm-read | nightingale | n9e-vm-read |
| observability-alerting | n9e-redis-auth | nightingale | nightingale-redis-auth |

Check the linked VM read tokens and ClickHouse ingest/hash relationship in memory, without printing values. Do not copy `obs-mtls-*`, old server CA/TLS Secrets, `cnpg-ca-secret`, `cnpg-webhook-cert`, `*-auto-secret`, Operator-generated VM configuration Secrets, or `sh.helm.release.*`. cert-manager creates new leaves; the reviewed public-CA projection step provides CA-only Secrets. Only the VM values currently reference `harbor-registry`; if another component needs image-pull credentials, add that explicit namespace scope after review rather than copying every old Secret.

### CRD preflight

Read-only inventory found 41 matching CRDs with `Established=True`. This is a point-in-time check, not a schema compatibility assertion. Compare the complete `.spec`/served versions from the locked packages against live definitions before installation and retain existing ownership:

- VM (`*.operator.victoriametrics.com`): `vlagents`, `vlclusters`, `vldistributed`, `vlogs`, `vlsingles`, `vmagents`, `vmalertmanagerconfigs`, `vmalertmanagers`, `vmalerts`, `vmanomalies`, `vmanomalyconfigs`, `vmauths`, `vmclusters`, `vmdistributed`, `vmnodescrapes`, `vmpodscrapes`, `vmprobes`, `vmrules`, `vmscrapeconfigs`, `vmservicescrapes`, `vmsingles`, `vmstaticscrapes`, `vmusers`, `vtagents`, `vtclusters`, `vtsingles`.
- Altinity: `clickhouseinstallations.clickhouse.altinity.com`, `clickhouseinstallationtemplates.clickhouse.altinity.com`, `clickhouseoperatorconfigurations.clickhouse.altinity.com`, `clickhousekeeperinstallations.clickhouse-keeper.altinity.com`.
- CNPG (`*.postgresql.cnpg.io`): `backups`, `clusterimagecatalogs`, `clusters`, `databaseroles`, `databases`, `failoverquorums`, `imagecatalogs`, `poolers`, `publications`, `scheduledbackups`, `subscriptions`.

VM values use `crds.enabled=false` **and** `crds.plain=false`. Altinity has true `crds/` package entries, so use `--skip-crds` and keep `crdHook.enabled=false`. CNPG CRDs are under `templates/crds/crds.yaml`, so its required switch is `crds.create=false`; `--skip-crds` alone would not protect them. No new render should contain a CRD. Do not pass `--take-ownership`.

### Render and post-renderer chain

The following writes only private review artifacts on the builder. It does not apply Kubernetes resources. `OBS_CANDIDATE` is supplied by the release coordinator after GREEN verification.

```bash
set -euo pipefail
umask 077
export OBS_SOURCE="${OBS_CANDIDATE:?set the verified builder candidate}/ops/observability/next"
obs_charts=/root/raytrain-observability-additive-20261008T065709Z/charts
obs_review=/root/raytrain-observability-certmanager-20261009T014000Z/deployment-review
mkdir -p "$obs_review"

cat > "$obs_review/vm-sds.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
exec python3 "${OBS_SOURCE:?}/mtls/sds_post_render.py" --namespace victoriametrics
SH
cat > "$obs_review/vm-operator-sds.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
{
  cat
  printf '\n---\n'
  cat "${OBS_SOURCE:?}/victoriametrics/operator-cluster-read.yaml"
} | python3 "$OBS_SOURCE/mtls/sds_post_render.py" --namespace victoriametrics
SH
cat > "$obs_review/ch-operator-sds.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
python3 "${OBS_SOURCE:?}/clickhouse/operator-post-render.py" |
  python3 "$OBS_SOURCE/mtls/sds_post_render.py" --namespace clickhouse
SH
cat > "$obs_review/cnpg-sds.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
python3 "${OBS_SOURCE:?}/nightingale/scope-cnpg-webhooks.py" |
  python3 "$OBS_SOURCE/mtls/sds_post_render.py" --namespace nightingale
SH
chmod 700 "$obs_review/"*-sds.sh

helm template victoriametrics-cluster "$OBS_SOURCE/victoriametrics/resources" \
  -n victoriametrics --kube-version 1.36.1 \
  -f "$OBS_SOURCE/victoriametrics/resources-hub-values.yaml" \
  --post-renderer "$obs_review/vm-sds.sh" > "$obs_review/vm-resources.yaml"
helm template victoriametrics-operator "$obs_charts/victoria-metrics-operator-0.68.1.tgz" \
  -n victoriametrics --kube-version 1.36.1 --skip-crds \
  -f "$OBS_SOURCE/victoriametrics/operator-values.yaml" \
  --post-renderer "$obs_review/vm-operator-sds.sh" > "$obs_review/vm-operator.yaml"
helm template clickhouse-operator "$obs_charts/altinity-clickhouse-operator-0.27.4.tgz" \
  -n clickhouse --kube-version 1.36.1 --skip-crds \
  -f "$OBS_SOURCE/clickhouse/operator-values.yaml" \
  --post-renderer "$obs_review/ch-operator-sds.sh" > "$obs_review/ch-operator.yaml"
kubectl kustomize "$OBS_SOURCE/clickhouse" |
  python3 "$OBS_SOURCE/mtls/sds_post_render.py" --namespace clickhouse > "$obs_review/ch-resources.yaml"
kubectl kustomize "$OBS_SOURCE/otel" |
  python3 "$OBS_SOURCE/mtls/sds_post_render.py" --namespace opentelemetry > "$obs_review/otel.yaml"
helm template cloudnative-pg-operator "$obs_charts/cloudnative-pg-0.29.1.tgz" \
  -n nightingale --kube-version 1.36.1 --skip-crds \
  -f "$OBS_SOURCE/nightingale/10-cnpg-values.yaml" \
  --post-renderer "$obs_review/cnpg-sds.sh" > "$obs_review/cnpg.yaml"
helm template nightingale-redis "$obs_charts/redis-ha-4.39.0.tgz" \
  -n nightingale --kube-version 1.36.1 --skip-tests \
  -f "$OBS_SOURCE/nightingale/30-redis-values.yaml" \
  --post-renderer "$OBS_SOURCE/nightingale/scope-redis.py" > "$obs_review/redis.yaml"
kubectl -n nightingale create configmap n9e-config-v911 \
  --from-file="$OBS_SOURCE/nightingale/config/render-config.py" \
  --from-file="$OBS_SOURCE/nightingale/config/config.toml.template" \
  --from-file="$OBS_SOURCE/nightingale/config/envoy.yaml" \
  --dry-run=client -o yaml > "$obs_review/nightingale-config-source.yaml"
{
  cat "$obs_review/nightingale-config-source.yaml"
  printf '\n---\n'
  cat "$OBS_SOURCE/nightingale/40-nightingale.yaml"
} | python3 "$OBS_SOURCE/mtls/sds_post_render.py" --namespace nightingale > "$obs_review/nightingale.yaml"
```

SDS must be last: the CNPG and ClickHouse renderers first add their Envoy containers/config, then SDS validates the final mount paths and adds the leaf discovery files. Redis uses native TLS and needs only `scope-redis.py`. Do not remove the specialized post-renderer and run only SDS. VM Operator's needed proxy ConfigMap belongs to the resources release; adding its narrow ClusterRole in the Operator post-renderer makes that new role belong to the correct Helm release instead of leaving an unmanaged ownership conflict.

Run existing scope/component contracts over these outputs, inspect every cluster-scoped name, and perform server-side dry-run/diff. The CRD allowlist must be empty for these new releases; independently review emitted ClusterRole/Binding and suffixed CNPG webhook names. Scope failures are not fixed by broadening permissions.

### Actual deployment order after review

For each rendered raw file, run `kubectl apply --dry-run=server -f FILE`, review the full `kubectl diff -f FILE`, then apply only that file. For each Helm release, use the same values and same post-renderer above with `helm upgrade --install`, `--skip-crds`, and `--wait --timeout 15m`; keep the archive path fixed. Save release revision and rendered/config hashes. Do not apply the Helm-rendered YAML separately.

1. Confirm cert-manager leaves are Ready and all required public CA projections/non-PKI Secrets exist. The canonical `20-postgres.yaml` must already reference the new explicit server TLS / server CA, with N9E and bootstrap projections matching it. Do not rely on an extra unpublished runtime patch.
2. Install `victoriametrics-cluster` first using the local resources chart and `vm-sds.sh`. Its CRs can exist before the Operator because CRDs already exist; this also creates `victoriametrics-cluster-operator-mtls` before the Operator Pod starts. Then install `victoriametrics-operator` with `vm-operator-sds.sh`. Wait for all generated VM workloads, then verify authenticated write/query and self targets.
3. Apply `ch-resources.yaml` before installing `clickhouse-operator`: the existing CRDs accept the Keeper/CHI CRs, and the needed proxy ConfigMaps/Services exist before the Operator starts. Install the Operator with `ch-operator-sds.sh`. Wait for Keeper **3** and ClickHouse **2** Ready Pods in `clickhouse`; confirm both replica paths and DNS match the candidate. Review/apply `clickhouse/bootstrap-job.yaml` once; wait for `job/clickhouse-schema-v1` Complete. Do not treat Pod Ready as schema completion.
4. Apply `otel.yaml` only after schema readiness. Wait for StatefulSet `otel-gateway` **2/2**, then send a fresh explicit canary and verify exact IDs on both CH replicas with queue depth returning to zero.
5. Install `cloudnative-pg-operator` with `cnpg-sds.sh`. Confirm two controller Pods Ready and the **suffixed** webhooks route to `nightingale`; test real admission in both new and old namespaces. Apply `nightingale/20-postgres.yaml`, then wait for `cluster/nightingale-postgres` Ready with **3** instances and synchronous replication. Install `nightingale-redis` with `scope-redis.py`; wait for **3** Redis/Sentinel members. Helm install does not run the test hook; do not run `helm test`.
6. Restore the verified old `n9e` database into the new PG **before** applying `nightingale.yaml`. Because this is a restoration, do **not** execute `35-bootstrap.yaml` or regenerate the seed: that one-time job is for an empty new application schema and intentionally rejects existing `users`. Revalidate restored role/password ownership and the datasource URL/token; the supported datasource URL remains the application's local read proxy `http://127.0.0.1:18427`.
7. Apply `nightingale/45-networkpolicy.yaml` as inactive defense-in-depth under unchanged CNI, then `nightingale.yaml` with its initial one replica. Verify database migrations, login, Redis/Sentinel and new VM read path. Apply `50-ha-patch.yaml` with `kubectl -n nightingale patch deployment nightingale --type=strategic --patch-file ...`; verify two Pods. Later config reapply must retain the tested two-replica state instead of inadvertently restoring bootstrap replicas=1.
8. Deploy the reviewed certificate rotation controller only after its RBAC and current object names/UID boundary are verified. Force one test renewal per reload class and confirm the actual consumer uses the new certificate, then compare old platform/training baseline. Keep old observability resources and data intact until the migration acceptance/backup window is complete.

The exact Helm write commands corresponding to the reviewed renders are below. Execute them at the numbered dependency gates above, **not as one unattended batch**. `--kube-version` and `--skip-tests` belong to the template commands and are deliberately absent here.

```bash
# Gate 2: resources first, then its Operator.
helm upgrade --install victoriametrics-cluster "$OBS_SOURCE/victoriametrics/resources" \
  -n victoriametrics -f "$OBS_SOURCE/victoriametrics/resources-hub-values.yaml" \
  --post-renderer "$obs_review/vm-sds.sh" --wait --timeout 15m
helm upgrade --install victoriametrics-operator "$obs_charts/victoria-metrics-operator-0.68.1.tgz" \
  -n victoriametrics --skip-crds -f "$OBS_SOURCE/victoriametrics/operator-values.yaml" \
  --post-renderer "$obs_review/vm-operator-sds.sh" --wait --timeout 15m

# Gate 3: only after reviewed ch-resources.yaml was applied.
helm upgrade --install clickhouse-operator "$obs_charts/altinity-clickhouse-operator-0.27.4.tgz" \
  -n clickhouse --skip-crds -f "$OBS_SOURCE/clickhouse/operator-values.yaml" \
  --post-renderer "$obs_review/ch-operator-sds.sh" --wait --timeout 15m

# Gate 5: CNPG, canonical PG CR, then Redis; check each dependency before proceeding.
helm upgrade --install cloudnative-pg-operator "$obs_charts/cloudnative-pg-0.29.1.tgz" \
  -n nightingale --skip-crds -f "$OBS_SOURCE/nightingale/10-cnpg-values.yaml" \
  --post-renderer "$obs_review/cnpg-sds.sh" --wait --timeout 15m
helm upgrade --install nightingale-redis "$obs_charts/redis-ha-4.39.0.tgz" \
  -n nightingale -f "$OBS_SOURCE/nightingale/30-redis-values.yaml" \
  --post-renderer "$OBS_SOURCE/nightingale/scope-redis.py" --wait --timeout 15m
```

Before retrying a failed release, inspect its exact manifest/status and any newly allocated PVCs. Do not add `--force`, `--take-ownership`, automatic namespace deletion, or cloud-volume cleanup to make a retry succeed.

### CRD compatibility receipt

The read-only builder comparison completed successfully on 2026-10-09. Evidence: `/root/raytrain-observability-certmanager-20261009T014000Z/crd-compatibility.json` (mode `0600`). It records the three actual chart archive SHA256 values, source archive members, every CRD UID/ownership before and after, and per-version schema hashes.

All **41 CRDs** (VM 26, CH/CHK 4, CNPG 11) were `Established=True`. All 41 schema hashes and complete normalized specs matched the locked charts; all ownership/UID/namespace metadata and live specs remained unchanged across the two reads. Only these 45 defaults were normalized on the chart side: 41 absent `spec.conversion` fields became `{strategy: None}`, and four Altinity absent `spec.names.listKind` fields became `<Kind>List`. These defaults match [Kubernetes v1.36.1 API defaulting](https://github.com/kubernetes/apiextensions-apiserver/blob/v0.36.1/pkg/apis/apiextensions/v1/defaults.go). No schema, served/storage version, or list-order differences were ignored. This clears the CRD reuse compatibility gate; it does not replace the new workload server-side dry-run or protocol acceptance gates.

### New-namespace protocol acceptance preparation

Builder candidate `476887165ebe5437c41dd62b39fec34d7a4b8835` is the source for the prepared probes. The preparation step generated files only; it did not create or execute a Kubernetes probe. Under `/root/raytrain-observability-certmanager-20261009T014000Z/`, `new-vm-live/` and `new-n9e-live/` each contain `probe.yaml`, `inventory.py`, `receipt.json`, and `commands-review.sh`. The receipt records exact canonical source hashes, image digest, Secret reference names, and pending dependencies. Commands require coordinator review before execution.

- VM Pod/ConfigMap: `victoriametrics/vm-live-d07e51a72f0d`. Its initial `targets.json` contains 17 current Ready VM Pods, selected by exact component/release labels. Refresh with `python3 inventory.py vm` immediately before the full probe; the probe reads this JSON from stdin. The temporary Pod mounts only existing VM leaf certificates and read/write tokens and writes one uniquely labelled synthetic metric.
- Nightingale Pod/ConfigMap: `nightingale/nightingale-live-d07e51a72f0d`. It uses canonical `live_probe.py`, `live_dependencies.py`, `frontend_probe.py`, and the checked-in upstream asset lock. The coordinator must first copy only the `ca.crt`, `tls.crt`, and `tls.key` leaf keys from `victoriametrics/obs-mtls-vmagent` to the unique temporary Secret `nightingale/nightingale-live-d07e51a72f0d-monitor`, in memory/stdin without writing or printing its data. This is not a signing-CA copy. PostgreSQL server trust comes from `obs-mtls-ca-postgres-server-public`; Redis auth comes from `nightingale-redis-auth`.
- Dependency targets require three Ready PostgreSQL instances and two Ready CNPG Operator Pods. Application targets require two Ready Nightingale Pods and an explicitly reviewed **existing** VM datasource ID. The application probe performs existing-root login/session/captcha operations, but creates no users, datasources, alerts, seed, or business configuration. A missing datasource may not be counted as a successful VM query test.
- Both temporary Pods request zero GPUs, use the existing low priority class with `preemptionPolicy: Never`, exclude `172.28.1.222` and `172.28.3.32`, disable service-account token mounting, use a read-only root filesystem and non-root UID, and expire after 1,800 seconds. Capture creation UIDs and delete only these exact temporary objects after acceptance; the deadline does not delete Pod/ConfigMap/Secret objects.

The old builder evidence root `/root/raytrain-observability-additive-20261008T065709Z/` retains `obs-vm-live-check.py` and `mtls-validation/a74c9c9-n9e-live/` as historical references. Do not directly execute their old namespaces, Pod IPs, generated target files, or datasource-creation script against the new stack. Native PostgreSQL 5432 authentication and actual API-server admission remain separate acceptance steps; `live_dependencies.py` explicitly records these as untested.

ClickHouse/OTel's reusable historical Pod templates are `mtls-validation/b4e8b1d-ch-operator-fix/{acceptance-pods.yaml,keeper-tls-probe.yaml,otel-acceptance-probes.yaml}`. No standalone complete live runner was found there. Regenerate unique temporary names and current DNS; replace `observability-schema-client` with `clickhouse-schema-client`. Verify roles independently: schema-admin can perform the reviewed schema operations, query-admin can SELECT but cannot write, and OTel writer can INSERT but cannot SELECT. Send unique OTLP log/trace IDs to **both** new gateways and query those exact IDs on **both** ClickHouse replicas, including the trace lookup table and queue-drain checks. Do not run `clickhouse/verify-runtime.py` or `otel/verify_runtime.py` against VKE: these are Docker-only harnesses. HTTP OTLP acceptance is not evidence of a real gRPC Export or live crash-recovery test.
