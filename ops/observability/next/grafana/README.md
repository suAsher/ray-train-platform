# Independent Grafana HA

This directory owns only the `grafana` namespace application resources, its exact certificate issuance policies, two dedicated PostgreSQL CA issuers, and a separately scoped CNPG operator. The original `monitoring/grafana` deployment, SQLite/PVC and browser gateway fallback remain unchanged. No private dashboards, users or SQLite data are migrated automatically.

Grafana 13.1.1 runs two replicas with shared native PostgreSQL state; no Redis or shared Grafana filesystem is needed for sessions/dashboards. Grafana Live WebSocket connections can reconnect across replicas; live channel state is process-local. Alert evaluation is disabled here because Nightingale remains the alerting owner. The ClickHouse plugin 4.22.1 is installed at image build time under `/opt/grafana/plugins`; plugin signatures remain required, no unsigned-plugin exception or runtime download is enabled. Both pods must share `GF_SECURITY_SECRET_KEY` to decrypt provisioned datasource credentials.

## Resource and ownership boundaries

| Workload | Count | CPU requests | Memory requests | Persistent disk |
|---|---:|---:|---:|---:|
| Grafana + query/UI Envoy | 2 | 250m each | 576Mi each | none |
| CNPG operator + metrics Envoy | 2 | 150m each | 320Mi each | none |
| PostgreSQL | 3 | 500m each | 1Gi each | 20Gi Retain each |

Total steady requests: 2.3 CPU, 4.75Gi memory, 60Gi storage, 0 GPU. All workloads use existing `raytrain-observability-additive` (`-1000`, `preemptionPolicy: Never`). Grafana and PostgreSQL require placement on separate eligible nodes; no GPU tolerations are added. The existing Retain StorageClass and PriorityClass are prerequisites, not re-owned here. Quota includes rolling-update CPU/memory headroom. HA does not replace database backups; configure approved CNPG backup/WAL archival before relying on recovery from storage loss.

PostgreSQL uses its own server and client CA. Reusing Nightingale's client CA would allow its `streaming_replica` certificate to authenticate replication in the other cluster, because PostgreSQL checks CN rather than SPIFFE URI. Separate issuers close that boundary. CNPG operator 8000 status is protected by the existing patched operator public-key pin; metrics 9187/18443 require the exact VMagent identity. CNPG admission 9443 uses server TLS plus a fixed API-server RemoteAddr allowlist (172.28.2.64,172.28.5.50), not client-certificate mTLS. A control-plane address change requires reviewing the inherited hardening image and validator.

## External Secret contract

No Secret values or private keys are rendered or committed.

| Namespace | Secret | Required keys / contents |
|---|---|---|
| cert-manager | obs-mtls-ca-grafana-postgres-client | dedicated CA `tls.crt`, `tls.key` |
| cert-manager | obs-mtls-ca-grafana-postgres-server | dedicated CA `tls.crt`, `tls.key` |
| grafana | grafana-db-app | Kubernetes basic-auth `username=grafana`, random `password` |
| grafana | grafana-admin | `admin-user`, random `admin-password`, stable random `secret-key` |
| grafana | grafana-vm-read | `token`, same independent read token as VMAuth grafana user |
| grafana | grafana-clickhouse-auth | `password`, SQL user `grafana_platform_query` |
| grafana | obs-mtls-ca-postgres-server-public | `ca.crt`, public dedicated server CA only |
| grafana | obs-mtls-postgres-client-ca | `ca.crt`, public dedicated client CA only |
| grafana | obs-mtls-cnpg-metrics-ca | `ca.crt`, public mesh CA only |

Give all three public CA Secrets `cnpg.io/reload=true`. Import public CA material using a restricted process that never prints private CA keys. `render.py --part certificates` creates exact Certificate/CertificateRequestPolicy declarations and policy-use grants for the existing cert-manager service account. Leaf Secrets are owned and rotated by cert-manager. This standalone identity contract intentionally avoids the existing global `*/mtls-identities.json` scanner, whose namespace allowlist remains unchanged.

## Builder-only render and build

Follow the repository release skill: review and commit locally, transport by Git bundle, then run these commands in the detached builder candidate. Do not overwrite the formal build directory.

```bash
python3 -m unittest discover -s ops/observability/next/tests -p test_grafana_ha.py -v
docker build --platform linux/amd64 \
  -t grafana-observability:13.1.1-clickhouse-4.22.1-candidate \
  ops/observability/next/grafana/image
```

After the authorized publisher pushes the built image, set `GRAFANA_IMAGE` to its verified Harbor tag **and registry digest**, then render (the default empty image fails closed):

```bash
python3 ops/observability/next/grafana/render.py \
  --values ops/observability/next/grafana/values.yaml \
  --image "$GRAFANA_IMAGE" > /tmp/grafana-application.yaml
helm template cloudnative-pg-operator <cloudnative-pg-0.29.1-chart> \
  --namespace grafana --skip-crds \
  -f ops/observability/next/grafana/cnpg-values.yaml \
  --post-renderer ops/observability/next/grafana/scope_cnpg.py > /tmp/grafana-cnpg.yaml
```

`render.py` owns application YAML as a complete declarative set; the CNPG chart release owns only its independent operator resources. `scope_cnpg.py` reuses reviewed Nightingale code and metrics proxy configuration without modifying either. All cluster RBAC names and webhook patch rights get a `-grafana` suffix; `WATCH_NAMESPACE=grafana`; shared CNPG CRDs remain separately owned. Never install this chart without its post-renderer. Existing global SDS post-rendering is unnecessary: both renderers directly produce filesystem SDS, retaining directory-mounted leaf Secrets.

Deployment order: bootstrap namespace; external Secrets and CA imports; certificates/policies and Certificate Ready; operator; dedicated PostgreSQL and Ready primary/standbys; Grafana and all datasource health checks; browser gateway change after review. First server-side dry-run both complete outputs and review ownership, namespace, Secret references and resources. The existing frontend gateway must use `https://grafana.grafana.svc:8443` and present its existing `nightingale/n9e-ui-gateway` certificate; Grafana accepts no direct plaintext cross-Pod browser traffic. The caller's HTTPS Host/Origin must pass unchanged through both Envoys, with WebSocket upgrades enabled. `rootURL` and `allowedOrigins` in values must match the actual browser entrypoints.

## Data paths and acceptance

- VM: Grafana Prometheus datasource → `127.0.0.1:18427` → mTLS VMAuth:8427; independent Grafana token permits only query/metadata API routes. No write or admin routes. This is the default metric datasource.
- ClickHouse: official plugin native protocol → `127.0.0.1:19000` → mTLS ClickHouse:9440; certificate role `grafana/grafana-query`, user `grafana_platform_query`, SELECT only on `raytrain_observability.otel_logs_all`, `otel_traces_all`, `otel_traces_trace_id_ts_all`. The local plugin TLS switch is false because its network peer is the loopback Envoy; remote transport always verifies the exact ClickHouse server URI and uses mTLS.
- Original Prometheus and Loki use their pre-existing Service HTTP endpoints as explicit fallback/native sources. Those older paths retain their existing security boundary.
- Provisioned production dashboard uses backend service `ray-train-backend`, the production Distributed tables, VM metrics and GPU utilization; no synthetic canary datasource is presented as business telemetry.

Require builder contract checks, Envoy config validation, signed-plugin startup verification, real PostgreSQL TLS/SCRAM success, bad cert/password/plaintext rejection, HA Pod/failover checks, native admin browser login, datasource health/query success, WebSocket/Origin checks and a saved-dashboard reload across replicas before switching the existing gateway. Do not stop training or reuse production identities for test traffic. Keep old Grafana intact for the gateway rollback; do not delete retained PostgreSQL PVC/PV on rollback. Files in this directory alone do not establish production deployment or acceptance.

The PostgreSQL/HA/signature lab is reproducible entirely on the builder:

```bash
python3 ops/observability/next/grafana/verify_runtime.py \
  --image grafana-observability:13.1.1-clickhouse-4.22.1-candidate
```

It uses the same PostgreSQL 17.11 image, an internal Docker network, short-lived test-only certificates and credentials, and automatically removes its own containers/network/data. It checks TLS/SCRAM, negative authentication, plugin signature, provisioning, cross-replica persistence, loss of one Grafana replica and Envoy configuration. It does not replace real cluster CNPG failover or browser/CH/VM acceptance. The ClickHouse account retains `readonly=1`; only `max_execution_time` may change within 1–30 seconds, matching `queryTimeout=30`, as required by the [official plugin client](https://grafana.com/docs/plugins/grafana-clickhouse-datasource/latest/configure/). SQL grants remain the authorization boundary.

## Dedicated CA bootstrap and recovery checks

These are maintenance steps for the authorized publisher on the builder, not automatic deployment hooks. Use a newly created root-only directory, retain its path in the private release record, and do not print, commit or copy its keys into the repository. Create two independent CA keypairs with `umask 077`; for each `client` and `server`, use a distinct descriptive CN and CA extensions (`basicConstraints=critical,CA:TRUE` and `keyUsage=critical,keyCertSign,cRLSign`). For example, with `CA_WORK` set to that restricted directory:

```bash
umask 077
for role in client server; do
  openssl req -x509 -newkey rsa:4096 -nodes -days 3650 \
    -subj "/CN=RayTrain dedicated Grafana PostgreSQL ${role} CA" \
    -addext 'basicConstraints=critical,CA:TRUE' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign' \
    -keyout "$CA_WORK/$role.key" -out "$CA_WORK/$role.crt"
done
```

Import each pair exactly once as a Kubernetes TLS Secret named `cert-manager/obs-mtls-ca-grafana-postgres-client` or `cert-manager/obs-mtls-ca-grafana-postgres-server`. Before creation, verify the name is absent; do not overwrite an existing CA during routine rollout. Pass certificate/key files directly to `kubectl create secret tls ... --cert=... --key=...`; do not use `kubectl get secret -o yaml` to move CA keys. Record only the public SHA-256 certificate fingerprints and validity dates. The `ClusterIssuer` declarations emitted by `--part certificates` refer to these exact Secret names.

Create `grafana/obs-mtls-postgres-client-ca` from the **client certificate file only**, and `grafana/obs-mtls-ca-postgres-server-public` from the **server certificate file only**, each with key `ca.crt`. The separate `grafana/obs-mtls-cnpg-metrics-ca` contains only the existing mesh CA public certificate. Label each `cnpg.io/reload=true`. Never reuse the dedicated PG client root as the mesh root or vice versa. Wait for both Issuers and all six Grafana Certificates to become Ready; compare the replication certificate issuer and the PostgreSQL `clientCASecret` fingerprint before database initialization.

The inherited Nightingale probe scripts hardcode its namespace and must not be pointed at this database without adaptation. The safe reusable portions are their certificate-rejection test cases and the CNPG API/status assertions. On the dedicated Grafana deployment, record the three instance names, primary, ready state, timeline/LSN, synchronous standby and the two Grafana Pod UIDs before a planned switchover. Select an existing healthy Grafana standby, use the installed CNPG plugin's `kubectl cnpg promote` for **grafana-postgres in namespace grafana only**, then verify one writable primary, both standbys rejoin, and a dashboard created before promotion remains readable from each Grafana replica. Do not change Nightingale's operator, database, or active training workloads for this check.

For database backup/restore, first identify the dedicated Grafana primary and a root-only backup destination, then use `pg_dump --format=custom` for database `grafana` through an authorized database identity. A dump contains authentication and encrypted datasource state and must remain private with mode 0600. Restore with `pg_restore --exit-on-error` into a separate disposable PostgreSQL instance/database; connect a disposable Grafana using the same externally maintained `secret-key` and validate the saved dashboard and datasource decryption. Never restore over the live database as an acceptance test. Retain the tested backup, CA custody and secret-key recovery instructions under the deployment's private recovery policy. CNPG failover, tested logical restore and future WAL/PITR backups are distinct evidence; the disposable local lab currently covers PostgreSQL TLS and Grafana replica loss, not a three-instance CNPG failover or live database backup.
