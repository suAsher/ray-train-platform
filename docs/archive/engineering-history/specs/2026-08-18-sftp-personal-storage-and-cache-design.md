# Personal IDC Storage, Harbor Registration, and NVMe Cache Design

**Status:** approved implementation direction — 2026-08-18

## Goal

Give each authenticated user a safe personal storage journey without revealing
TOS credentials: connect their existing IDC SFTP home, copy data in either
direction to their governed TOS space, debug and train against the selected
logical locations, register immutable images from their own Harbor project,
and accelerate transient data with the two NVMe disks on each GPU node.

## Decisions

### Personal IDC data is transferred through SFTP, not mounted in Ray Pods

`<user>@mount.wellspiking.ai` is an individual writable storage account. The
platform will not use SSHFS in a debug or training Pod: that would require a
user credential in a general-purpose GPU workload and would make FUSE mounts
part of the training trust boundary.

Instead, the Data Migration service creates a narrowly scoped Kubernetes Job
in the caller's tenant namespace. The Job mounts only the caller's already
governed TOS PVC and an ephemeral, read-only Secret containing one private
SFTP key. It uses SFTP copy operations between a clean relative IDC path and a
clean relative `我的文件` path. It receives no TOS AK/SK and no general cloud
credential. Initial jobs use copy (never destructive sync); the result records
file count, byte count, checksums when available, and failed paths.

The backend generates the user's Ed25519 keypair at enrollment. The browser
can copy the public key so the user adds it to the IDC storage system. The
private half is written once to a namespace-scoped Kubernetes Secret, mode
`0400`, and is never returned by an API, written to PostgreSQL, stored in TOS,
logged, or mounted by a Ray Pod. The SFTP host, port, and pinned `known_hosts`
entry are deployment configuration; the user cannot submit an arbitrary host,
port, endpoint, or absolute remote root.

SFTP accounts are bound to `(tenant, authenticated subject)`. The shown
username comes from the account binding, not a caller-supplied host string.
The binding initially accepts an RFC-constrained remote username and is marked
`pending`; a successful authenticated connectivity check marks it `ready`.
Tenant administrators can view its status but not the private key.

### IDC NFS is a separate, read-only dataset catalogue

The supplied NFS exports may contain organization-wide data. The platform must
not mount their global roots in all user workloads. Operators register a
specific read-only export or a bounded sub-export as a named shared data space,
then grant it to a tenant. A future folder-level catalogue must resolve user
access before returning a listing. Personal writable IDC data continues to use
the SFTP transfer channel until a per-user NFS export contract exists.

### Harbor image discovery is self-service registration, not a platform build

Users push with their own credentials to their personal project under
`harbor.wellspiking.ai/guofeng.su/...`. The Portal accepts only an immutable
digest reference under the configured Harbor host/prefix. It never receives a
Harbor password. A registration request is pending until a tenant administrator
publishes it into the existing image catalogue; only published references are
selectable for debug and training. Kubernetes continues using its deployment
pull Secret, never an end-user credential.

### NVMe is cache, never durable user storage

Each GPU node has two independent empty 3.5 TiB ext4 volumes: `/data1` and
`/data2`. No disk is reformatted and no RAID/LVM is created. The platform will
create node-local cache directories on both volumes and expose them through a
node-aware local PV/CSI class after a render and binding test. A Ray workload
gets an ephemeral cache claim at `/mnt/cache`; Ray temp/object spill and
explicit dataset staging can use it. TOS and IDC remain sources of record, so
cache eviction or a node failure never loses user data.

## User-visible flow

1. On **我的数据 → 数据迁移**, the user binds their IDC account, copies the
   generated public key, adds it to their IDC storage account, and clicks
   **验证连接**.
2. The user chooses `IDC 个人目录` and `我的文件` through directory browsers,
   not raw paths. They select direction and press **开始迁移**.
3. The migration detail page shows queued/running/succeeded/failed state,
   copied bytes/files, failure details, and a link to the resulting logical
   TOS directory.
4. The user develops in `/workspace`, reads/writes personal TOS mounts,
   publishes an immutable snapshot, chooses a published Harbor digest, and
   creates a 1/8/16 GPU task. Output appears below **我的训练结果**.

## Security and operational rules

- Every location is `(logical space, clean relative path)`; reject `..`,
  leading slashes, backslashes, empty components, NUL bytes, raw bucket names,
  and raw SFTP locations.
- Pinned server host keys are mandatory. Do not use trust-on-first-use.
- Migration Jobs run non-root, have a restricted service account, resource
  limits, active deadline, retry policy, and TTL cleanup after their durable
  result is recorded.
- APIs authorize caller ownership before create, list, inspect, retry, or
  cancel. Tenant administrators never gain access to a user's private key.
- Transfer metadata and audit logs redact remote credential material and do
  not show raw infrastructure paths to end users.
- New interfaces have user-scoped rate/concurrency limits. Copy operations are
  idempotent per request ID and do not delete either side.

## Required deployment inputs before live SFTP activation

1. Fixed `mount.wellspiking.ai` SFTP hostname and port.
2. A verified `known_hosts` line (or SSH host-certificate CA) for that server.
3. Confirmation that each user's SFTP account accepts the platform-generated
   public key, and the mapping rule or approved per-user remote username.
4. Kubernetes encryption at rest enabled for Secrets, and RBAC permitting only
   the platform backend and the scoped transfer Job to read those Secrets.

None of the SFTP private material belongs in TOS, PostgreSQL, Git, Helm values,
or a training/debug container.
