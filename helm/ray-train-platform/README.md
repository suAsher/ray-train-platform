
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
