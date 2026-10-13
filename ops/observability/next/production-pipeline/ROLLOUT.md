# Production HTTP telemetry rollout

This procedure is a release-owner checklist. Preparing manifests and server-side dry-runs does not deploy them. Run on the authorized builder from a tested, detached Git-bundle candidate; record its full SHA and the current Kubernetes context. Do not read or export existing Secret values, generated ClickHouse user/cluster ConfigMaps, or private keys for backups.

## Prepare and review

1. Keep the backend emitter disabled until storage and the gateway have passed their gates. Record both CH Pod UIDs, StatefulSet revisions, Ready replicas, active endpoints and replication health. Record gateway UIDs/revisions and queue depth. Do not change Keeper, operator, storage classes, training resources or existing PVCs.
2. Confirm the existing canary schema names: database `observability_canary`; tables `otel_logs`, `otel_traces`, `otel_traces_trace_id_ts`, `otel_logs_all`, `otel_traces_all`, `otel_traces_trace_id_ts_all`; view `otel_traces_trace_id_ts_mv`. The production database is separately `raytrain_observability`. The old `clickhouse-schema` ConfigMap and `clickhouse-schema-v1` Job are outside this rollout. The old Job may already have been removed by its TTL.
3. Provision the four Secret references listed in [README.md](README.md). For the existing `clickhouse-auth`, append only the three new hash keys with a reviewed JSON Patch; record their names for rollback. Do not replace the Secret, copy existing credentials out, print credentials, or place values in shell arguments/history. Verify availability through metadata and consumer startup, without exporting Secret bodies.
4. Render with the SDS post-renderer last. It preserves the running certificate delivery mechanism. Raw Envoy bootstrap manifests are not the production artifact.

```sh
umask 077
release_dir=$(mktemp -d /tmp/rtp-platform-release.XXXXXX)
git rev-parse HEAD > "$release_dir/candidate-sha.txt"
kubectl config current-context > "$release_dir/context.txt"
kubectl kustomize ops/observability/next/clickhouse |
  python3 ops/observability/next/mtls/sds_post_render.py > "$release_dir/clickhouse-sds.yaml"
kubectl kustomize ops/observability/next/otel |
  python3 ops/observability/next/mtls/sds_post_render.py > "$release_dir/otel-sds.yaml"
kubectl kustomize ops/observability/next/production-pipeline > "$release_dir/schema.yaml"
```

Select only the CHI, ClickHouse proxy ConfigMap and ClickHouse ingress policy from the broader ClickHouse render. Add the operator reconciliation guard below. Preserve the unchanged live gateway PVC template and Envoy `POD_IP` field reference, including API-defaulted fields; existing client-side ownership otherwise conflicts with SSA. This preparation reads workload configuration only and performs no writes to the cluster.

```sh
python3 - "$release_dir" <<'PY'
import copy, json, pathlib, subprocess, sys, yaml
out = pathlib.Path(sys.argv[1])
selected = {('ClickHouseInstallation', 'clickhouse'),
            ('ConfigMap', 'clickhouse-proxy-clickhouse'),
            ('NetworkPolicy', 'clickhouse-ingress')}
ch = [d for d in yaml.safe_load_all((out / 'clickhouse-sds.yaml').read_text())
      if d and (d['kind'], d['metadata']['name']) in selected]
assert {(d['kind'], d['metadata']['name']) for d in ch} == selected
chi = next(d for d in ch if d['kind'] == 'ClickHouseInstallation')
chi['spec']['reconcile'] = {
    'runtime': {'reconcileShardsThreadsNumber': 1},
    'statefulSet': {'update': {'onFailure': 'abort', 'timeout': 600},
                   'recreate': {'onDataLoss': 'abort', 'onUpdateFailure': 'abort'}},
    'host': {'wait': {'exclude': True, 'queries': True, 'include': True,
                     'replicas': {'all': 'yes', 'new': 'yes', 'delay': 10},
                     'probes': {'readiness': 'yes'}}}}
(out / 'clickhouse-changes-guarded.yaml').write_text(yaml.safe_dump_all(ch, sort_keys=False))
live = json.loads(subprocess.check_output(
    ['kubectl', '-n', 'opentelemetry', 'get', 'sts', 'otel-gateway', '-o', 'json']))
otel = [d for d in yaml.safe_load_all((out / 'otel-sds.yaml').read_text()) if d]
sts = next(d for d in otel if d['kind'] == 'StatefulSet')
# This rollout does not change queue storage. Review semantic equality first.
sts['spec']['volumeClaimTemplates'] = copy.deepcopy(live['spec']['volumeClaimTemplates'])
old_envoy = next(c for c in live['spec']['template']['spec']['containers'] if c['name'] == 'envoy')
new_envoy = next(c for c in sts['spec']['template']['spec']['containers'] if c['name'] == 'envoy')
pod_ip = next(e for e in old_envoy['env'] if e['name'] == 'POD_IP')
new_envoy['env'] = [copy.deepcopy(pod_ip) if e['name'] == 'POD_IP' else e
                    for e in new_envoy['env']]
(out / 'otel-sds-preserve-storage.yaml').write_text(yaml.safe_dump_all(otel, sort_keys=False))
PY
kubectl apply --server-side --dry-run=server -f "$release_dir/clickhouse-changes-guarded.yaml" -o name
kubectl apply --server-side --dry-run=server -f "$release_dir/otel-sds-preserve-storage.yaml" -o name
kubectl apply --server-side --dry-run=server -f "$release_dir/schema.yaml" -o name
kubectl diff --server-side -f "$release_dir/clickhouse-changes-guarded.yaml"
kubectl diff --server-side -f "$release_dir/otel-sds-preserve-storage.yaml"
kubectl diff --server-side -f "$release_dir/schema.yaml"
```

`kubectl diff` returns 1 for an expected difference. Review every difference; never use `--force-conflicts` to bypass unexplained ownership conflicts. Compare existing CHI grants with the candidate, preserving all old users/grants and any separately managed SQL access storage. The expected changes are the three production SQL users/profiles, internal remote-reader cluster/env reference, guarded reconciliation, Grafana native listener identity, ingress rules, independent platform receiver/exporter and port, and the new schema bootstrap objects. Do not apply the complete ClickHouse kustomization just to add these changes.

## ClickHouse: one replica at a time

The observed topology has one shard and two replicas, represented by two **single-replica** StatefulSets: `chi-clickhouse-telemetry-0-0` and `chi-clickhouse-telemetry-0-1`. The Altinity operator owns them. A PDB does not serialize their controller-driven updates.

The pinned [Altinity 0.27.4 host reconciliation](https://github.com/Altinity/clickhouse-operator/blob/0.27.4/pkg/controller/chi/worker-reconciler-chi.go) processes hosts serially within a shard, recovers unhealthy replicas first, and returns on a non-deferred error. The [reconciliation settings](https://github.com/Altinity/clickhouse-operator/blob/0.27.4/docs/chi-examples/99-clickhouseinstallation-max.yaml) above explicitly request one shard worker, readiness, replica synchronization and cluster inclusion waits, and abort on update/recreation failures. Keep these settings in the applied release artifact. If the first host cannot become healthy, investigate it before continuing; do not restart its peer manually or remove guards to complete a rollout.

After release approval, apply the reviewed proxy ConfigMap/policy and guarded CHI as one planned change. Updating the new password environment reference changes the Pod template, and each host must restart once. Envoy's added Grafana URI is static bootstrap configuration, so XML user hot reload alone is insufficient. Do **not** additionally issue `rollout restart` or modify child StatefulSet partitions: that can add unnecessary restarts or conflict with the operator.

During the operator rollout, observe both StatefulSet/Pod states concurrently, require at least one healthy endpoint, and verify that a restarted host is Ready and replication catches up before its peer is disrupted. After both complete, require CHI reconciliation `Completed`, both StatefulSets' current/update revisions equal, both replicas Ready, no readonly/expired replication sessions, empty or falling replication queues, and canary continuity. New production tables do not exist yet, so this health gate uses existing canary replication. Keep the established small background pools; the isolated test verified merges and TTL with those same settings.

## Schema, gateway and application gates

Apply only `schema.yaml` after CH credentials/configuration and both replicas pass their gate. Wait for `job/clickhouse-platform-schema-v1` in namespace `clickhouse` to complete; its own ConfigMap is `clickhouse-platform-schema`. It reuses the existing `clickhouse-schema-client`, bootstrap password reference and schema-admin leaf. Review the Job result without printing credentials. Verify all seven new tables/views on both replicas, their Replicated/Distributed engines, materialized-view definer, trace/lookup TTL 30 days and log TTL 7 days. Repeated v1 SQL is idempotent; a later schema revision requires a new reviewed Job name.

Apply the reviewed gateway objects next. The `otel-gateway` StatefulSet has two replicas and ordinary ordered rolling updates; preserve the original queue PVCs. Its changed environment/port template triggers the needed rollout, so no second restart is required. Wait for both replicas Ready and current/update revisions equal. Verify original canary exports, new port 14318 mTLS acceptance only for the backend URI, denied wrong methods/paths/roles, and platform export success. Confirm queues drain without sustained export failure.

Enable the backend emitter and Grafana datasource only after these gates. Verify actual backend binary OTLP HTTP request completion spans/logs and trace lookup correlation; inspect only the documented allowlisted fields. Grafana must query all three Distributed tables with `grafana_platform_query`, while direct local-table reads, writes, sensitive system tables and arbitrary remote table functions remain denied. `prefer_localhost_replica=0` and the dedicated remote-reader identity are required together; keep the existing `telemetry` cluster secret unchanged. Confirm unchanged training Pod/RayJob identities and health through the release owner's broader acceptance checks.

## Rollback

Disable the backend emitter first. Retain/drain Collector queues before removing the platform pipeline or its ingest credentials. Restore reviewed previous nonsecret gateway/ClickHouse manifests, using the same one-host-at-a-time guard for any rollback requiring a Pod change. Retain all CH data, production tables, queue PVCs and existing SQL access storage; no DROP, REVOKE of historical grants, or inverse schema operation is part of rollback.

Remove only the three newly added `clickhouse-auth` hash keys if all new consumers/configuration have been removed and no pending data still needs them. Similarly retire only the newly provisioned consumer Secrets when unused, using the release owner's approved credential procedure. Do not restore or overwrite the whole historical Secret. Keep nonsecret render/diff/dry-run and result evidence, and delete this rollout's transient materials after the release closes.
