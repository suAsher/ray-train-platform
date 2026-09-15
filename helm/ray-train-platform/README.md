
### Dedicated training nodes

`training.dedicatedNodes` maps tenant IDs to node hostname labels, for example:

```yaml
training:
  dedicatedNodes:
    algorithm: ["172.28.3.32"]
```

New training submitters, heads, workers, and interactive workspace heads/workers
use required node affinity. Assigned tenants can only use their listed nodes;
other tenants exclude every dedicated node. Existing RayJobs and RayClusters are
not patched. The normal tenant GPU quota and submission APIs are unchanged.
A missing or full assigned node cannot fall back to the shared pool.

Before admitting a dedicated node to the training pool, set
`platform.wellspiking.ai/dedicated-tenant=<tenant>:NoSchedule`. Only new workloads
of that tenant receive the exact toleration. This also prevents pre-cutover
RayCluster templates from recreating a worker on the newly dedicated node.
Do not use `NoExecute` or restart existing workloads during cutover.

Deploy the backend assignment and the node-onboarding controller/chart support
first. The separate onboarding chart needs the inverse whitelist
`dedicatedNodeTenants: {"172.28.3.32": "algorithm"}`. Then taint the node before
adding `accelerator=nvidia-rtx-4090` and
`platform.wellspiking.ai/gpu-pool=production`. Let onboarding verify both local
disks and NFS and set `platform.wellspiking.ai/cache-ready=true`; do not set that
receipt manually. Keep the shared Kueue flavor and queues: TAS evaluates the
required affinity and node taints before assigning a topology domain. Verify the
installed Kueue version supports these constraints. Removing an assignment is a
separate capacity migration: retain its taint until affected active templates
and queued workloads have been reviewed.

#### Shanghai A cutover — 2026-09-15

The `algorithm` assignment to `172.28.3.32` was enabled with source
`7ad97a85a1b8512864bc15d493a7044a98b7098c`. Backend release revision 240 uses
`sha256:2a3bac8f5349aee86abfa3a219da0b8c5021beb0eada1d891789344fca58fd17`;
node-onboarding revision 9 uses
`sha256:ff15d1b3441c4a627309baa986fda63636997d84cac47016c42dcfc641c3371d`.
The node carries the production/accelerator labels, the dedicated-tenant label
and `NoSchedule` taint, and a controller-issued cache-ready receipt after local
PVC read/write and NFS checks. The team quota remains 8, its queue remains
`algorithm-gpu`, and Kueue preemption remains disabled. Total physical pool
capacity is 40 GPUs; other teams cannot consume the dedicated eight.

Validation used the unchanged live Kueue v0.19 shared flavor/queue and placement
fields emitted by the production renderer. In a temporary namespace, two
zero-GPU owner probe Pods ran on `172.28.3.32`; two shared probe Pods ran on
`172.28.1.229`. A conflicting E-zone owner request stayed unadmitted due to hard
affinity, and a legacy template targeting A stayed unadmitted due to its missing
taint toleration. The initial probe fixture lacked its non-preempting
PriorityClass; it was corrected before the successful run. No user workload was
modified. Full backend tests, Helm render contracts and all 61 server admission
checks passed. New placement/toleration helpers have 100% statement coverage;
the new configuration parser has 93.8%.

This is live scheduling isolation acceptance, not an end-to-end GPU training
acceptance. Existing algorithm jobs consume its eight-GPU quota, and the build
host's saved `spk-rayjob` authentication was expired. Run a normal authenticated
GPU submission after quota is available to verify the full training runtime.
Release overlays, sanitized before/after resource evidence and probe reports
are retained on the build host under
`/root/raytrain-dedicated-node-20260915/`.
The temporary namespace and its Jobs/Workloads/Pods were deleted after the
successful run. All 13 pre-existing active RayJob/RayCluster/Pod objects retained
their UIDs, spec hashes, states, node placement and container restart counts.
