# Native certificate consumption

cert-manager renews each role's leaf Secret. Envoy consumes the files through SDS;
this document covers verification and controlled renewal consumption for
ClickHouse Keeper, ClickHouse, Redis/Sentinel, Nightingale and Alertmanager gossip.
Native server and client reload behavior differs by component; a changed Secret
alone does not establish what each running connection uses. CloudNativePG has its
own certificate reload path and is excluded from this eviction controller.

A small, dedicated CronJob checks the fixed `rotation-policy.json` every 15
minutes. It starts **suspended**. Its Python image digest, generated StatefulSet
names, PDB names and all readiness conditions must be verified before enabling it.
Deploy the script and policy as a ConfigMap named `native-certificate-rotation`
with keys `rotation.py` and `rotation-policy.json`; the manifest does not embed
private certificates or bootstrap credentials.

The order is Keeper → ClickHouse → Redis/Sentinel → Nightingale → Alertmanager.
One Pod is evicted at a time using the Kubernetes `policy/v1` Eviction API and a
UID/resource-version precondition. Each group must have all expected replicas
Ready before each eviction. Its exact named PDB must cover exactly those Pods,
have current status, and permit a disruption. The code does not delete Pods,
modify StatefulSet/Deployment templates, bypass a PDB, or access training
namespaces. A stopped or expired Lease holder must reacquire ownership before any
mutation. API calls use the in-cluster CA and projected service-account token.

The first run treats every unrecorded Pod as unknown and replaces it serially.
After a replacement with a new UID is Ready, and the certificate snapshot remains
unchanged, the Pod receives an annotation containing only aggregate public
certificate hashes. A subsequent run skips matching Pods. A failed run resumes
from the annotated replacements; unknown existing Pods are not assumed to have
loaded a certificate. A blocked PDB, non-Ready replica, operator rollout,
certificate change during replacement, unexpected parallel replacement, Lease
loss or timeout stops the run without evicting another Pod.

The default 130-second stable-secret interval allows the normal kubelet Secret
cache/projection interval to settle before replacing an application. This is a
conservative operational safeguard, not proof of a remote application's TLS
state. Initial rollout acceptance must inspect native TLS connections/peer
membership and confirm that the new certificate is used. A Ready Pod alone is
not a cryptographic proof. The automated controller records a completed orderly
restart; it does not claim to perform protocol-level native certificate probing.

Only leaf renewal is automatic. The policy pins the SHA-256 of each approved
public `ca.crt` (base64-decoded PEM bytes with trailing ASCII whitespace stripped).
The repository template intentionally contains unfilled pins: deployment must
fill them from previously approved public CA material. Missing, unfilled or
mismatched pins stop before eviction, including first bootstrap and the case where
all Pod annotations disappeared. Do not learn the trust pin from whatever Secret
happens to be present on a scheduled run. CA replacement requires a separate
overlapping-trust migration.
Alertmanager is a special consumer: VMOperator copies gossip TLS material into
`vmalertmanager-victoriametrics-cluster-config`. The controller reads only this
additional named Secret, compares its public certificate and CA with the source
Secret, checks that `gossip_config.yaml` references those exact generated files,
and verifies that the native Alertmanager container mounts and uses that config.
It accepts the operator's documented trailing-whitespace normalization. A stale
copy, alternate config path or merely mounting the original source Secret cannot
satisfy the gate. The generated Secret's private-key value is not inspected.

The controller never reads a signing-key Secret. Kubernetes GET of a leaf Secret
necessarily returns its private-key field, but the script does not inspect,
hash, persist or print that field; API error bodies are also suppressed. RBAC
permits GET only on the listed leaf Secrets. Pod-list/annotation/eviction rights
are restricted to the three new product namespaces because Kubernetes RBAC does
not support selector-based Pod permissions.

Enable only after the root deployment procedure verifies the policy against the
live objects, pins the image, confirms all PDB budgets, and runs a forced renewal
acceptance. Keep at least 30 days between renewal and expiry. Job failure must be
visible to the monitoring system so a blocked rotation is repaired before leaf
expiry. Suspension or a permanently blocked PDB means renewal will not reach
these native consumers automatically; do not describe that state as complete
rotation automation.

## Controlled live acceptance — 2026-10-09

The additive stack at candidate `476887165ebe5437c41dd62b39fec34d7a4b8835`
passed a real six-role leaf renewal and a subsequent no-op run. This is completed
controlled acceptance; the periodic CronJob remains **suspended**. The new
Alertmanager still has a blackhole receiver, so a real rotation-failure
notification channel is not connected. Do not enable unattended eviction until
that separate operational gate is complete.

Evidence is retained on the approved builder under
`/root/raytrain-observability-certmanager-20261009T014000Z/`.
The `rotation-deployment/native-acceptance-result.json` index and
`rotation-deployment/artifact-manifest.json` record the native checks and hashes.

- All six Certificates moved from revision 1 to revision 2 with new serials,
  unchanged approved CA pins and unchanged identities: Keeper, ClickHouse,
  Redis/Sentinel peer, Nightingale PostgreSQL client, Nightingale Redis client
  and Alertmanager gossip. Source/generated gossip assets, actual owners,
  mounts, readiness and the five named PDBs passed before the Job.
- `native-cert-baseline-20261009034136` completed the serial baseline. All 13
  component Pods acquired new UIDs and matching public-certificate annotations;
  native replication/quorum checks passed after replacement. No PDB was bypassed.
- The native probes verified the renewed server serials on 19 endpoints:
  Keeper 3 × 2281/9234, ClickHouse 2 × 9440/9010, Redis/Sentinel 3 × 6379/26379,
  and Alertmanager 3 × 9094. Keeper/Alertmanager public-CA-only TLS observations
  also received the explicit anonymous `certificate_required` alert; these
  observations prove server certificate identity and anonymous rejection,
  while separate native state checks establish healthy peer membership.
- PostgreSQL `pg_stat_ssl` observed the renewed client serial on the actual
  application connections from both new Nightingale Pod IPs. The Redis client
  check combines new Pod UIDs/annotations, the exact mounted client certificate,
  application health and authenticated Redis/Sentinel connections using that
  current configuration. It does not claim a Redis server-observed application
  client serial.
- Alertmanager had agreeing three-peer membership after replacement. Its
  originally empty silence sets remained empty; persistence of a nonempty
  silence was not tested. At 03:47:47 UTC, before its three Pod UIDs changed,
  native gossip servers already presented the revision-2 certificate. This
  demonstrates server-side reload in that observation, not that every outbound
  client context hot-reloads or that gossip servers always require a restart.
- `rotation-deployment/noop-result.json` records the next Job completing at
  04:04:22 UTC with all 13 Pod UIDs and annotations unchanged, all Pods Ready and
  zero container restarts. `cronSuspended` remained true.
- `new-ch-otel-live/post-result.jsonl` records read-only exact canary counts
  (2 logs, 2 traces, 2 trace lookups) on both physical ClickHouse replicas, stable
  repeat reads and empty OTel export queues/in-flight counts.
  `new-vm-live/post-result.json` records 182 passing VM checks.

Detailed native reports are
`rotation-deployment/native-postcheck-20261009T040403Z.json` and
`rotation-deployment/peer-native-public-serial-20261009T040359Z.json`.
The latter has an explicit nine-endpoint aggregate pass; process exit alone is
not substituted for inspecting individual checks. The before/after public
serials and Pod UIDs are in `rotation-deployment/renewal-baseline.json` and
`rotation-deployment/renewal-issued.json`.

## Reproducing online acceptance

The procedure below remains a reusable sequence for approved repeat acceptance.
The 2026-10-09 run renewed all six leaf roles before one serial baseline Job;
the single-role Redis example below is useful for a narrower future renewal.

Run only from the approved builder and the tested candidate checkout. Keep the
CronJob suspended throughout acceptance. These commands affect only the new
observability stack. The original monitoring stack and training Pods are outside
this procedure. Save the pre/post Pod UID inventory and application protocol
results; completion of a Kubernetes Job alone is not the acceptance result.

### 1. Fill approved CA pins and check actual consumers

Use the already approved CA certificate files; never derive trust anchors from a
freshly observed leaf Secret. This writes only a public-hash policy into the
restricted evidence directory. Include this policy and the tested `rotation.py`
in the reviewed ConfigMap/Helm deployment before proceeding.

```bash
umask 077
export OBS_CONTEXT='cd9q3b7rf9aidnnbd0je0@81533555-kd9q3q0frjmp21sk5lklg'
export OBS_SOURCE="$PWD/ops/observability/next/mtls"
export OBS_PKI='/root/raytrain-observability-pki-20261008'
export OBS_RUN='/root/raytrain-observability-certmanager-20261009T014000Z/rotation-acceptance'
install -d -m 0700 "$OBS_RUN"
python3 - "$OBS_SOURCE" "$OBS_PKI" "$OBS_RUN" <<'PY'
import hashlib, importlib.util, json, pathlib, sys
source, pki, out = map(pathlib.Path, sys.argv[1:])
sp = importlib.util.spec_from_file_location('rotation', source/'rotation.py')
m = importlib.util.module_from_spec(sp); sp.loader.exec_module(m)
policy = json.loads((source/'rotation-policy.json').read_text())
domains = {'obs-mtls-keeper':'mesh', 'obs-mtls-clickhouse':'mesh',
 'obs-mtls-redis-ha-peer':'redis', 'obs-mtls-nightingale-redis':'redis',
 'obs-mtls-n9e-postgres-client':'postgres-client', 'obs-mtls-alertmanager-gossip':'am-gossip'}
for group in policy['groups']:
 group['expected_ca_sha256'] = {s:hashlib.sha256((pki/(domains[s]+'.crt')).read_bytes().rstrip()).hexdigest()
                                for s in group['secrets']}
m.validate_policy(policy)
with (out/'rotation-policy.json').open('x') as f:
 json.dump(policy, f, indent=2); f.write('\n')
print('Approved public CA pins prepared; no private key accessed')
PY
```

The following is read-only. It runs the same source-identity, generated-asset,
Pod ownership, mounting, full-readiness and PDB checks against **every** component
before starting a disruptive baseline. It prints only component names and UIDs.
If a chart-generated PDB name or selector differs, correct the reviewed policy
and corresponding named RBAC entry; never relax the PDB check.

```bash
python3 - "$OBS_SOURCE" "$OBS_RUN/rotation-policy.json" "$OBS_CONTEXT" <<'PY'
import importlib.util, json, pathlib, subprocess, sys, urllib.parse
source, policy_file, context = sys.argv[1:]
s = importlib.util.spec_from_file_location('rotation', pathlib.Path(source)/'rotation.py')
m = importlib.util.module_from_spec(s); s.loader.exec_module(m)
class ReadOnly:
 def get(self, path):
  r = subprocess.run(['kubectl','--context',context,'--request-timeout=20s','get','--raw',path],
                     capture_output=True, timeout=25)
  if r.returncode: raise RuntimeError('Read-only Kubernetes request failed')
  return json.loads(r.stdout)
 def list_pods(self, namespace, selector=None):
  q = '?' + urllib.parse.urlencode({'labelSelector':','.join(f'{k}={v}' for k,v in selector.items())}) if selector else ''
  return self.get(m.core_path(namespace,'pods')+q)['items']
api = ReadOnly(); checker = m.Rotator(api, None)
for group in m.validate_policy(json.loads(pathlib.Path(policy_file).read_text()))['groups']:
 m.certificate_state(api, group)
 pods, _ = checker.snapshot(group); checker.check_pdb(group, pods)
 print(group['name'], [(p['metadata']['name'], p['metadata']['uid']) for p in pods])
PY
```

Also compare the deployed ConfigMap's `rotation-policy.json` and `rotation.py`
with these reviewed files, check the CronJob's fixed image and `suspend: true`,
and verify its ServiceAccount can GET only the named leaf/generated Secrets,
not CA signing Secrets, and cannot delete Pods or access training namespaces.

### 2. Establish the initial baseline with one explicit Job

The first run deliberately replaces unrecorded Pods one at a time. Check native
quorum/replication before this run and after it. A PDB denial, Job failure or
certificate mismatch is a stop condition; do not retry by deleting Pods.

```bash
OBS_BASELINE_JOB="native-cert-baseline-$(date -u +%Y%m%d%H%M%S)"
kubectl --context "$OBS_CONTEXT" -n cert-manager create job "$OBS_BASELINE_JOB" \
  --from=cronjob/native-certificate-rotation
kubectl --context "$OBS_CONTEXT" -n cert-manager logs -f "job/$OBS_BASELINE_JOB" \
  --pod-running-timeout=60s
kubectl --context "$OBS_CONTEXT" -n cert-manager get job "$OBS_BASELINE_JOB" \
  -o jsonpath='{.status.conditions}{"\n"}'
```

Require `Complete=True`, all five component completion messages, all expected
replicas Ready, new UIDs for initially unrecorded Pods, matching public-hash
annotations, healthy native protocols and unchanged training/old-monitoring UIDs.
Run a second explicit Job after the 120-second Lease expires: it must complete
without changing any component Pod UID. An API `403`, `429`, timeout or PDB with
zero budget is not a successful baseline. Retain Job logs/UIDs in the evidence
directory; clean up only the explicitly named acceptance Jobs after capture.

### 3. Renew one role and prove native Redis/Sentinel consumption

Start with `obs-mtls-redis-ha-peer`: ports 6379 and 26379 are the native servers,
so their certificate serials prove more than the Envoy listener's serial. The
Nightingale container already has the appropriate Redis client certificate and
its own Redis credentials. Its verified image includes Python 3.14 and `tomllib`.
Create this probe file in the evidence directory; it prints no passwords, keys or
configuration text. Run it before and after renewal with the expected public
certificate serial as its sole argument.

```bash
cat > "$OBS_RUN/redis-native-check.py" <<'PY'
import json, pathlib, socket, ssl, sys, tomllib

def reply(f, depth=0):
 assert depth < 4
 line=f.readline(65537); assert len(line)<=65536 and line.endswith(b'\r\n')
 kind, data=line[:1], line[1:-2]
 if kind==b'+': return data.decode()
 if kind==b'-': raise RuntimeError('Redis command rejected')
 if kind==b':': return int(data)
 if kind==b'$':
  n=int(data); assert 0<=n<=1048576
  data=f.read(n); assert len(data)==n and f.read(2)==b'\r\n'
  return data.decode()
 if kind==b'*':
  n=int(data); assert 0<=n<=32
  return [reply(f,depth+1) for _ in range(n)]
 raise RuntimeError('Unsupported Redis response')

def command(sock, stream, *parts):
 fields=[p.encode() for p in parts]
 sock.sendall(b'*'+str(len(fields)).encode()+b'\r\n'+b''.join(
  b'$'+str(len(p)).encode()+b'\r\n'+p+b'\r\n' for p in fields))
 return reply(stream)

def main():
 cfg=tomllib.loads(pathlib.Path('/run/n9e/etc/config.toml').read_text())['Redis']
 ctx=ssl.create_default_context(cafile=cfg['TLSCA']); ctx.minimum_version=ssl.TLSVersion.TLSv1_3
 ctx.load_cert_chain(cfg['TLSCert'],cfg['TLSKey'])
 expected=int(sys.argv[1],16); roles=[]; masters=[]
 for i in range(3):
  host=f'nightingale-redis-announce-{i}.nightingale.svc'
  for port in (6379,26379):
   with socket.create_connection((host,port),timeout=8) as raw:
    with ctx.wrap_socket(raw,server_hostname=host) as conn, conn.makefile('rb') as stream:
     serial=conn.getpeercert()['serialNumber']; assert int(serial,16)==expected
     password=cfg['Password'] if port==6379 else cfg['SentinelPassword']
     assert command(conn,stream,'AUTH',password)=='OK'
     if port==6379:
      info=dict(line.split(':',1) for line in command(conn,stream,'INFO','replication').splitlines()
                if line and not line.startswith('#') and ':' in line)
      roles.append(info['role'])
      if info['role']=='master': assert int(info['connected_slaves'])==2
      else: assert info['role']=='slave' and info['master_link_status']=='up' and info['master_sync_in_progress']=='0'
     else:
      assert command(conn,stream,'SENTINEL','CKQUORUM','n9e').startswith('OK ')
      master=command(conn,stream,'SENTINEL','get-master-addr-by-name','n9e')
      masters.append((socket.gethostbyname(master[0]),master[1]))
     print(json.dumps({'host':host,'port':port,'serial':serial,'authenticated':True}))
 assert sorted(roles)==['master','slave','slave'] and len(set(masters))==1
 print('PASS: 6 native TLS serials; 1 master + 2 synced replicas; 3 Sentinels agree with quorum')
try: main()
except Exception:
 print('FAIL: native Redis certificate/replication/quorum check',file=sys.stderr); sys.exit(1)
PY
OBS_N9E_POD=$(kubectl --context "$OBS_CONTEXT" -n nightingale get pods \
  -l app.kubernetes.io/name=nightingale -o jsonpath='{.items[0].metadata.name}')
redis_serial() {
  kubectl --context "$OBS_CONTEXT" -n nightingale get secret obs-mtls-redis-ha-peer \
    -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -serial | cut -d= -f2
}
OBS_OLD_SERIAL=$(redis_serial)
kubectl --context "$OBS_CONTEXT" -n nightingale exec -i "$OBS_N9E_POD" -c nightingale \
  -- python3 -B - "$OBS_OLD_SERIAL" < "$OBS_RUN/redis-native-check.py"
```

Use the official, checksum-verified `cmctl v2.6.1` binary prepared under the
release's `tools/cmctl-v2.6.1/` directory, not a floating download or a fabricated
Certificate status patch. `renew` marks one Certificate for reissuance; it does
not prove that consumers loaded the new certificate.
[Official cmctl renewal documentation](https://cert-manager.io/docs/reference/cmctl/#renew).

```bash
OBS_CMCTL='/root/raytrain-observability-certmanager-20261009T014000Z/tools/cmctl-v2.6.1/cmctl_linux_amd64'
OBS_OLD_REV=$(kubectl --context "$OBS_CONTEXT" -n nightingale get certificate \
  obs-mtls-redis-ha-peer -o jsonpath='{.status.revision}')
"$OBS_CMCTL" --context "$OBS_CONTEXT" renew -n nightingale obs-mtls-redis-ha-peer
```

Poll the Certificate every 5 seconds for at most 5 minutes. Require `Ready=True`,
`status.revision > OBS_OLD_REV`, and `redis_serial` different from
`OBS_OLD_SERIAL`; checking Ready alone can incorrectly accept the old revision.
Check that the pinned CA hash remains unchanged. Then start one explicit Job as
above, with a fresh `native-cert-renewal-<timestamp>` name. Keep the CronJob
suspended until this completes and record the before/after UIDs: only the three
Redis/Sentinel Pods should change for this isolated role renewal.

```bash
OBS_NEW_SERIAL=$(redis_serial)
test "$OBS_NEW_SERIAL" != "$OBS_OLD_SERIAL"
kubectl --context "$OBS_CONTEXT" -n nightingale exec -i "$OBS_N9E_POD" -c nightingale \
  -- python3 -B - "$OBS_NEW_SERIAL" < "$OBS_RUN/redis-native-check.py"
```

After the Job, require the probe's six serial matches and quorum result, successful
Nightingale `/ping`, and all unrelated component/old-monitoring/training Pod UIDs
unchanged. Repeat no-client-certificate and wrong-CA negative tests from
`nightingale/live_dependencies.py`; a reset/timeout alone does not prove denial.
Other native roles still need their own acceptance: Keeper leader/followers and
Raft convergence, ClickHouse replicated-table queues, Alertmanager's agreeing
three-peer mesh and preserved silences, Nightingale's authenticated PG/Redis
connections. Redis success does not certify those roles. Only after their
protocol evidence and the failure alert are recorded should the reviewed Helm
values enable the schedule; do not simply unpause it on Job completion.
