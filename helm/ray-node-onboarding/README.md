# Node onboarding controller

Install this chart separately in the **existing `ray-cache-local` namespace** on
Kubernetes 1.30 or later. Defaults render no objects. The chart does not create a
namespace, change the platform API service account, or expose a network service.

Supply the controller's built immutable `image`, the immutable `helperImage`, and
`nfs.shares` with 1–8 reviewed `{server, path}` entries. NFS exports are always
mounted read-only. The controller runs one replica with Recreate plus a leader
Lease; all probes use the separate tokenless `node-onboarding-probe` account.

Before activation, clone each complete live provisioner ConfigMap into the
corresponding `data1ConfigMap` / `data2ConfigMap`. Preserve `config.json`, setup,
teardown, helperPod.yaml and collect-cache-metrics. This chart creates only the
NFS source ConfigMap and an initially empty protected proof store, never runtime
cache maps. Switch the two cache releases to
their external maps separately using `existingConfigMap` after reviewing the
rendered changes. Controller updates may change only runtime `config.json`.

Use two rollout stages:

1. Install with `enabled: true`, `activateController: false`. This creates the
   eight mandatory Fail/Deny admission policies, bindings and RBAC with **zero**
   controller replicas. Check every policy's status/type-check expression
   warnings on the actual API server; an offline Go-template or CEL compilation
   does not substitute for server type checking.
2. As the controller requester, verify server-side dry-run permits the exact
   generated prepare/probe Pods, 1Gi smoke PVCs, own Lease and state-only Node
   patch. Negative checks must reject altered images/commands, added containers,
   env/lifecycle commands, alternate hostPaths, writable NFS, SA token mounting,
   extra capabilities, privileged/hostPID/hostNetwork, cordon/taint changes,
   foreign Node labels/annotations/finalizers/owners, script ConfigMap edits,
   unrelated PVCs and deletion of unowned resources. Verify other service
   accounts, including existing provisioner helpers, remain unaffected.
   Only after these checks and security review set `activateController: true`.

Any command/image/NFS-source change requires repeating the staged checks; the
policy enforces the complete current fixed commands and image digests. Admission
policies match **requester identity**, not a removable object label. Resource
labels and Node ownership are validated rather than used to skip validation.
PVC admission accepts Kubernetes' automatic `kubernetes.io/pvc-protection`
finalizer but no caller-chosen finalizers.

The proof store `node-onboarding-state` starts without a `data` field so Helm
upgrades preserve controller-owned `state.json`; `helm.sh/resource-policy: keep`
also preserves it on uninstall. While bound, its policy rejects deletion and
forged data from every other requester. Intentional retirement therefore requires
explicitly removing the proof-store binding first. The readiness label is likewise
protected from other Node writers, while normal cordon and other Node management
remain allowed. Preparations add only read-only `/sys/dev/block` and `/sys/devices`
mounts for physical-device ancestry proof; no full `/sys` mount is permitted.
Both probe kinds use `preemptionPolicy: Never`, the default scheduler and cluster
DNS, without custom DNS or host aliases.

To pause, set `activateController: false`; this preserves policies, external
maps, the NFS configuration and existing cache data. Do not remove ready gating
or clear newly registered mappings as an incidental rollback. This chart does
not cordon, uncordon, evict, or modify existing training workloads.

Local contracts: `bash tests/render-contract.sh` (set `HELM` for a non-PATH
binary) and `cd ../../backend && go test ./config -run TestNodeOnboardingChart`.
Production API-server policy checks and probe success remain required before
activation.

The executable server contract is `tests/server-admission-contract.py`. Generate
its four fixture files from the actual controller constructors using the backend
`TestAdmissionFixtures` test, with `NODE_ONBOARDING_FIXTURE_CONFIG` pointing to a
JSON file and `NODE_ONBOARDING_FIXTURE_DIR` to a new empty temporary directory.
The JSON shape is:

```json
{
  "Config": {
    "Namespace": "ray-cache-local", "ConfigNamespace": "ray-cache-local",
    "ProbeServiceAccount": "node-onboarding-probe",
    "Data1ConfigMap": "ray-cache-local-data1-runtime",
    "Data2ConfigMap": "ray-cache-local-data2-runtime",
    "Data1StorageClass": "ray-cache-local-data1",
    "Data2StorageClass": "ray-cache-local-data2",
    "Image": "<actual controller image@sha256:digest>",
    "HelperImage": "<actual helper image@sha256:digest>",
    "NFSConfigMap": "node-onboarding-nfs", "StateConfigMap": "node-onboarding-state"
  },
  "NodeName": "<actual node>", "NodeUID": "<actual Node UID>",
  "Hostname": "<actual kubernetes.io/hostname>",
  "Shares": [{"server": "<actual NFS server>", "path": "/actual-export"}]
}
```

Run from `backend`: `go test ./nodeonboarding -run '^TestAdmissionFixtures$' -count=1 -v`
with those environment variables, then run
`python3 helm/ray-node-onboarding/tests/server-admission-contract.py <fixture-dir>`
from the repository root using an administrative kubeconfig on the target cluster.
Use `--context` to select the intended context and override the two runtime map
names or provisioner SA if needed. It impersonates the controller for scoped
operations, tests other requester boundaries, and refuses any mutation missing
`--dry-run=server`. No probe Pod, PVC or GPU workload is persisted. A failed
expected denial is accepted only when the intended policy name appears in the
server error, so an unrelated RBAC error cannot falsely pass the test.
