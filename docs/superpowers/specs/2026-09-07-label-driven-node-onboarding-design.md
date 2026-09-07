# Label-driven GPU node onboarding

Status: simplified workflow approved; revised technical design pending review.
Automatic cache preparation/registration is not implemented or deployed yet.

## Operator contract

Join Kubernetes, install working GPU drivers/device-plugin, persistently mount
two independent data disks at `/data1` and `/data2`, then run:

```bash
kubectl label node <node-name> \
  accelerator=nvidia-rtx-4090 \
  platform.wellspiking.ai/gpu-pool=production --overwrite
```

The accelerator label is for the current 4090 pool, not other GPU models.
The platform prepares cache storage, registers both disks, validates them and
automatically adds eligible GPU capacity to Kueue. Administrators allocate team
quota through the existing UI. No manual Kueue edits, SSH keys or wizard.
The controller never cordons, uncordons, drains or restarts nodes/workloads.

## Components and safety gate

A separate, default-disabled controller and dedicated service account own
onboarding; public API permissions are not expanded to host operations.
The two operator labels express intent. The controller manages the additional
`platform.wellspiking.ai/cache-ready=true` label after successful verification.
Operators do not set it. Add this label to the configured selector shared by
new training/debug workloads and capacity calculation. Never retroactively patch
existing RayJobs, RayClusters, Pods or PVCs. Previously created pending workloads
must be identified during migration; new selectors do not protect old objects.

Reconcile by Node name and UID with leader election/resource-version checks.
Expose bounded step/status/reason through annotations and Events, surfaced in
the existing administrator UI. No new multi-step wizard or manual acceptance.

## Automatic sequence

1. Verify production labels, Node Ready, non-virtual node and allocatable GPUs.
2. Run a fixed, resource/time-bounded preparation Pod. Read host mount metadata
   to prove `/data1` and `/data2` are independent mounted block devices, neither
   the host root device nor symlinks. Container bind mounts alone are not proof.
3. Create only missing `ray-cache` directories as root:root 0770. Existing roots
   must satisfy the contract; report mismatches without recursive chmod/chown.
   Never format disks, replace symlinks, delete user data or use root-disk fallback.
4. Merge the node into both provisioner runtime maps, retaining unrelated nodes
   and the empty deny-default entry. Partial registration cannot mark ready.
5. Create temporary node-bound PVCs for both actual StorageClasses. Verify each
   PV's node/path and UID/GID 1000 write/read. Clean up only owned probe resources
   with UID preconditions; verify reclamation before claiming success.
6. Recheck Node UID, labels and both registrations, mark ready, and observe
   existing automatic Kueue convergence. Do not grant team quota automatically.

Probes use no GPUs. Readiness does not certify physical NVMe throughput, CUDA
compatibility or multi-node training performance; those require training tests.

## Configuration ownership and security

Add an opt-in external ConfigMap reference to the cache chart. Seed two new
runtime ConfigMaps from complete live configurations, preserving all mappings
and helper scripts, then switch the provisioners to these external configs.
The controller owns only runtime `config.json` mappings. Helm must not overwrite
them on upgrades. Preserve legacy configs/backups; rollback must retain nodes
registered after migration. Do not directly mutate Helm-managed maps.

Scope RBAC to the two runtime config names and a dedicated probe namespace.
Use immutable images, fixed commands, no SSH secrets, no SA token in probes,
no privileged/hostPID mode and no host root mount. Writable host access is limited
to the two data mounts; host mount metadata is read-only. Admission policy must
constrain Pod templates and Node patch fields: RBAC alone cannot enforce these.
No API may accept arbitrary images, commands, paths or labels for execution.

## Failures and rollout

Failure keeps the ready label absent and shows actionable reasons with bounded
retries. Readiness invalidation blocks new work but never deletes provisioner
mappings or cache data, and never evicts running work. Zero eligible nodes must
produce zero admission capacity, not preserve a stale positive budget.

Before activation, capture active workload UIDs/restarts and inspect pending
workloads. Install restricted controller/probes, migrate configuration, validate
existing and new nodes, then enable the shared ready selector. Existing nodes
are not assumed ready merely because they carry production labels. Validate
`172.28.1.229` without re-cordoning it; the user deliberately released it.
Observe 16-to-24 GPU convergence and unchanged training UIDs/restarts. Abort on
unexpected manifest changes or failed storage verification.

## Tests and documentation

Cover label-only rejection, successful ready admission, missing/same/root disks,
symlinks, permissions, no free space, GPU-less nodes, changed UID, concurrent
updates, restart, partial registration, malformed maps, delayed provisioners,
both real PVC smokes, cleanup failures and zero-node capacity. Verify upgrade
and rollback preserve dynamic mappings and old training objects are unchanged.

Update UI usage docs and `docs/OPERATIONS_GUIDE.md` with the two-label command,
persistent mounts, automatic status, recovery steps and the separate scope of
multi-node training verification. Team quotas remain administrator-controlled.
