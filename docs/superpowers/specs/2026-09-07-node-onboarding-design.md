# SuperAdmin node onboarding wizard (superseded)

Superseded by `2026-09-07-label-driven-node-onboarding-design.md`. The user
explicitly requested mounted disks plus labels, with automatic preparation,
not this multi-step wizard. This document is retained as design history only.

Status: scope approved; implementation awaits review of this design. This is
separate from the admin queue/quota display hotfix.

## Outcome and boundary

A SuperAdmin can onboard an already joined Kubernetes GPU node without manually
editing Kueue or consulting an operator for the checklist. The wizard does not
install the OS, join Kubernetes, format disks, install drivers, or migrate an
existing training workload. It never stores SSH private keys.

## Workflow

1. Discover nodes and show physical GPU count separately from eligible training
   capacity and Kueue's observed quota. Explain every exclusion.
2. Start a node-UID-bound operation. Require interactive Local/OIDC SuperAdmin,
   explicit confirmation, and no active training/debug workloads on the target.
   Cordon the target without draining any Pod.
3. Check Ready, GPUs, production-compatible hardware, required CSI/FSX/DNS,
   actual independent mounted block devices at /data1 and /data2, capacity and
   trusted ownership. Missing mounts are blockers, not directories to create.
4. Prepare only missing /data1/ray-cache and /data2/ray-cache directories with
   the established root:root 0770 contract. Never replace symlinks, chmod user
   trees recursively, format devices or erase existing contents.
5. Produce two independent cache mapping diffs preserving every existing node.
   Apply via a restricted release executor to the two named Helm releases;
   do not directly patch Helm-managed ConfigMaps. Persist release revisions,
   change ownership and configuration versions to detect concurrent edits.
6. Verify target-bound non-root dual-PVC write and reclamation, required actual
   data access, then an explicitly authorized resource-limited 1-GPU smoke.
   Cross-node training readiness requires a separate opt-in multi-node smoke.
7. Recheck Node UID, no conflicting operation, latest configuration and all
   required results before enabling the explicit release-for-training action.
   Uncordon, then observe automatic Kueue capacity synchronization. A sync error
   is shown as incomplete, not success. Team quota remains a separate UI action.

## Isolation and state

The API only creates/reads persisted operations. An independently deployed,
default-disabled controller and release executor own mutation privileges.
Per-node leases serialize operations; each step is restartable and idempotent.
Store actor, node UID, phase, timestamps, input versions, fixed probe image
digest, bounded logs, resource UIDs, Helm revisions and failures in the database.
Authentication and authorization are checked server-side for every action;
PATs, Engineers and TenantAdmins cannot initiate node mutation.

Short-lived Jobs use fixed templates, commands, paths and immutable images, with
resource/time limits, no SA token, no privileged/hostPID mode and no rootfs host
mount. Host filesystem inspection must prove host device/mount identity; a
container mount existing alone is insufficient. Use narrow host-mount metadata
access where necessary, never an arbitrary host command interface.

Kubernetes RBAC alone cannot constrain Job templates or Node patch fields.
Admission policies must enforce the permitted paths, images, node scope and
mutable fields before enabling the feature. Separate identities restrict release
operations to the two cache releases and verification resources; platform API
must not receive general Helm credentials or cluster-wide Job execution rights.

## Failure behavior and acceptance

Failure keeps the node cordoned. Clean up only resources with matching operation
and recorded UIDs; API/permission errors are failures, not evidence of deletion.
Never delete shared caches or auto-rollback an earlier node registration after
concurrent changes. Retries reconcile actual state before continuing.

Tests cover role boundaries, changed Node UID, duplicate starts, interrupted
steps, symlink/root-disk rejection, altered templates, shared mapping retention,
partial Helm failures, stale reports, probe cleanup failures, and 16-to-24 GPU
automatic quota convergence after releasing a third validated node. Production
acceptance must leave existing training Pod UIDs/restarts unchanged.
