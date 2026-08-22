# Production Data and Training Experience Design

**Status:** approved by platform owner on 2026-08-17

## Goal

Turn the existing Ray platform into a production-ready training service: users select governed data and code through the Portal, debug on a GPU, submit 1/8/16-GPU RayJobs, inspect logs and metrics, and keep all durable work in their own TOS space without ever receiving TOS credentials.

## Production topology

The two `cn-shanghai-e` RTX 4090 nodes form the production GPU pool. Each node supplies eight GPUs; a production job may use one GPU, one 8-GPU worker, or two 8-GPU workers (16 GPUs total). The existing one-GPU node remains a separately labelled smoke-test pool and must not be selected by production jobs. GPU admission is capped at the production-pool capacity and Kueue remains the sole admission authority.

The three CPU nodes host Portal frontend/backend, the existing PostgreSQL, KubeRay/Kueue controllers, Loki gateway/ingesters, Prometheus, Grafana, and control-plane services. Pod anti-affinity and PDBs keep stateless platform components available during one CPU-node loss; PostgreSQL is explicitly the accepted V1 single point of failure.

## Storage contract

The Portal exposes logical roots only; no browser, CLI, or workload sees a bucket name, TOS credential, NFS server, or raw cluster claim.

| Logical space | User-visible location | Runtime mount | Access |
| --- | --- | --- | --- |
| My workspace | TOS personal `workspace/` | `/workspace` | Read/write; source of immutable snapshots |
| My files | TOS personal `files/` | `/mnt/storage/me` | Read/write |
| My runs | TOS personal `runs/` | `/mnt/storage/me/runs` | RayJob output only |
| Team/public | governed TOS prefixes | `/mnt/storage/team`, `/mnt/storage/public` | Read-only to jobs; managed publication only |
| IDC shared datasets | registered NFS exports | `/mnt/idc/*` | Read-only |
| IDC personal data | one platform-owned full NFS export | not mounted wholesale in user Pods | User-specific subdirectory only, via data gateway / transfer Job |
| Node cache | NVMe local CSI volume | `/mnt/cache` | Ephemeral cache/spill only; never source of record |

For the unified personal IDC export, only the internal data gateway mounts the export root. It derives a caller's NFS subdirectory from the authenticated subject using an operator-supplied template, validates every relative path, lists only that subtree, and creates a short-lived, scoped transfer Job. Training and debug Pods never mount the full export. Enabling this feature requires the NFS server, export path, and deterministic subject-to-directory template; the chart rejects partial configuration.

TOS is the durable storage platform. Per-user ObjectSet quotas remain the enforcement layer for personal TOS roots. The UI can browse/upload/create folders under a user's permitted logical root but deliberately never offers data download. Training reads and writes through per-user FSX/TOS mounts and has no AK/SK in its Pod.

## User journeys

1. **Prepare data.** In “我的数据”, users see My workspace, My files, My runs, and permitted shared/IDC spaces. A data-distribution panel shows source, size/count where known, transfer state, and cache availability. A user selects a personal IDC directory and a destination under My files; the server validates both and submits a transfer. Shared/public data is populated only by an administrator through the same governed publication workflow.
2. **Debug.** A Dev Workspace starts on one GPU from an approved Harbor image. `/workspace`, personal files, team/public and read-only IDC datasets appear at documented paths. `python -m venv /workspace/.venv` persists with the workspace; package caches use writable personal/cache locations. OS packages never persist and require a custom image.
3. **Submit code in TOS.** Users save code under My workspace, select a directory, and publish an immutable snapshot. The job wizard selects the snapshot, an approved image, a 1/8/16-GPU profile, logical input/checkpoint/output directories, and its command. The source materializer copies the snapshot to `/workspace`; a RayJob gets read-only inputs and an isolated run output directory.
4. **Submit externally.** `rayctl` packages the external directory, obtains a short-lived Portal upload URL using a PAT, waits for immutable artifact creation, and submits exactly the same job contract. An external node therefore needs only Portal network reachability and a user PAT; it needs neither cluster access nor TOS credentials.
5. **Operate.** The task detail page streams Loki logs (including post-completion retention) and shows Prometheus/DCGM GPU, CPU, memory, cache and job metrics. The user adjusts parameters by creating a new immutable job revision; running jobs are not mutated.

## Image contract

All runnable images are immutable digest references under `harbor.wellspiking.ai/guofeng.su`. The image catalog distinguishes workspace and training images, framework/CUDA metadata, default status, and compatibility notes. Users can select approved catalog images. A future self-service image build creates a constrained CPU build Job from a selected workspace snapshot and pushes to a tenant-scoped Harbor repository using a platform-managed robot secret; it never accepts registry credentials from a browser. Until that robot credential is configured, the UI clearly directs users to request an administrator-published image instead of suggesting runtime `apt` installation.

## NVMe cache design

`/data1` and `/data2` remain independent disks. A local CSI/LVM provisioner must create `ray-cache-local` volumes only on the node where the Ray Pod schedules. A Ray Pod receives a generic ephemeral PVC at `/mnt/cache`; Ray temp directories and object spill files use it. Dataset staging is opt-in and checksum-addressed; caches can be evicted without data loss. Direct `hostPath`, node-root mounts, and RAID across nodes are prohibited.

## Security and reliability rules

- All storage selectors are `(logical space, clean relative path)`, never raw URI/path/server/claim input.
- Authorization happens before browse, upload, snapshot, transfer, mount resolution, status, logs, artifacts and retry operations. All database queries remain parameterized.
- User Pods run non-root, do not receive cloud credentials, cannot mount host paths, and receive only approved images with digest pinning.
- The gateway applies user-scoped concurrency/rate limits and logs audit events without secrets.
- Transfer Jobs are namespace-scoped, receive only a user subpath plus the user's TOS mount, and are TTL-cleaned after status collection.
- Prometheus/Grafana and Loki live on CPU nodes; DCGM exporter is a DaemonSet on every GPU node. Alloy remains an API-discovery log collector unless host/container runtime logs are explicitly required.

## Explicit deployment inputs

The platform can implement and test all TOS, compute, image-catalog, external submission, and observability paths without further input. Activating personal IDC migration requires these non-secret deployment values:

1. `idcPersonalNFS.server` — NFS hostname/IP.
2. `idcPersonalNFS.path` — non-root exported directory.
3. `idcPersonalNFS.userPathTemplate` — fixed path template such as `users/{subject}`; `{subject}` is a path-safe platform identity, never client input.
4. A Harbor robot secret only when self-service custom-image builds are enabled.

## Acceptance criteria

- A non-admin cannot list, mount, upload to, transfer from, or submit a path outside their logical data spaces.
- A production 16-GPU job places two 8-GPU workers only on the two production nodes and completes a distributed smoke workload.
- A user can create a TOS workspace snapshot, submit it from the UI and `rayctl`, and see its output only in their My runs root.
- A Dev Workspace sees the documented data mounts, persists `.venv`, exposes a GPU worker editor, and does not retain OS-level package changes after recreation.
- Prometheus scrapes DCGM and platform metrics; job details show completed-job logs and GPU metrics within the documented retention period.
- The deployment render, backend/unit tests, frontend tests, single-GPU smoke, eight-GPU smoke, sixteen-GPU smoke, and data-mount smoke all pass before production handoff.
