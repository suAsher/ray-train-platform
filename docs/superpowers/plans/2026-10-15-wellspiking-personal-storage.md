# Wellspiking Personal Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Follow the ownership boundaries below and preserve concurrent changes.

**Goal:** Provide owner-only Wellspiking personal file access and a default writable debug-worker mount with unique per-user keys.

**Architecture:** Backend-owned stable user bindings and Secrets feed a fixed-origin SFTP adapter. A separate CSI node/FUSE component exposes only that user's personal root; Portal consumes explicit capabilities and never assumes training eligibility.

**Tech Stack:** Go/Gin/GORM/PostgreSQL, SSH/SFTP, CSI/gRPC/FUSE, Vue/Element Plus in FDE dev, Kubernetes retained static PV/PVC.

## Task 1 — Personal binding and file-store boundary

Files: backend/personalstorage/{types,policy,service,sftp,vault}.go and corresponding tests; backend/repositories/personal_storage.go; backend/db/migrations/0059_personal_storage.up.sql; migration version contracts. Own only this package, repository and migration work. Repository identity is stable user ID, not tenant. Define shared exported interfaces before consumers integrate.

- [x] Write regression tests for independent keys, stable owner binding, wrong owner/username, excluded shared roots, absolute/traversal/symlink paths, host-key rejection and bounded upload.
- [x] Commit only new tests into the isolated candidate and send a Git bundle to builder; observe missing-feature RED with `go test ./personalstorage ./repositories -run PersonalStorage`.
- [x] Implement the personal-only service and fixed-endpoint SFTP adapter; no remote root/host/user override from HTTP.
- [x] Run focused GREEN plus real two-user SFTP fixture. Verify migration 59 new/repeat/upgrade against isolated PostgreSQL and preserve versioned fixtures.

## Task 2 — CSI and FUSE mount

Files: backend/personalfs/, backend/personalcsi/, backend/cmd/personal-storage-csi/, images/personal-storage-csi/, helm/ray-train-platform/templates/personal-storage-csi.yaml. Coordinate dependencies through Task 1 owner; do not edit API or workspace renderer.

- [x] Write tests for lookup/readdir/read/write/mkdir and exclusion of other roots/links; NodePublish rejects mismatched owner/binding/generation and paths outside a kubelet publish target.
- [x] Observe RED on the builder before implementing.
- [x] Implement identity/probe/node CSI calls and per-volume FUSE instances. Do not expose a network CSI endpoint or mount credential files into user Pods. Return actual remote errors and close connections/unmount on cancellation.
- [x] Exercise two real SFTP accounts from isolated FUSE mounts on builder. `ls`, bidirectional writes, unmount/remount and wrong credentials must give real evidence. Test daemon restart/target ownership and record unsupported semantics explicitly.

## Task 3 — API, config and workspace integration

Files: backend/api/personal_storage*.go, backend/api/data_spaces.go, backend/api/workspaces.go, backend/api/jobs.go, backend/config/personal_storage.go, backend/main.go, backend/k8s/personal_storage.go, backend/k8s/raycluster.go, backend/domain/data_space.go, backend/domain/data_location.go, helm/ray-train-platform/{values.yaml,templates/backend-deployment.yaml}.

- [x] Test interactive-only owner derivation, disabled feature, no target-user input, space capabilities and all existing training entry points rejecting the new space.
- [x] Test that only the debug worker receives the PVC; no Secret, FUSE device, capability or privilege is added to head/worker; unconnected users retain existing behavior.
- [x] Observe missing behavior RED; implement explicit provider dispatch and mount readiness checks without weakening FSX/NFS contracts.
- [x] Run API/RBAC/render regression and full Go gates with PostgreSQL on builder. Feature stays disabled in defaults; enabling requires pinned driver image and known host data.

## Task 4 — FDE Portal

Files under src/views/rayTrain/: DataCache, Devcenter, api/personalStorage.js, dataSpaceActions.js/dataSpaceReadiness.js and narrow tests. Read current FDE AGENTS, development-spec and ui-style-guide. Use fresh FDE dev; never publish the old backend/frontend directory.

- [x] Write tests for per-user connection state, public-key-only display, capability-driven upload/browse, no training/preview/download buttons and rejection of stale team/user responses.
- [x] Observe RED in builder; implement one-time enrollment/verify and mount status using returned paths rather than hardcoded IDC labels.
- [x] Run focused tests, Dockerfile.lint and pnpm build:dev on the exact candidate. No push or deployment in this task without a concrete reviewed candidate and current authorization.

## Task 5 — Review, isolated evidence and delivery

- [x] Independent code/security review: upstream account isolation is the privacy boundary; filtering is not a substitute. Review Secrets, public responses, host keys, owner CAS, symlink semantics, cancellation and CSI node privilege.
- [x] Full gofmt/vet/Go/PostgreSQL gates on builder; meaningful coverage of new behavior; report skipped production cases separately.
- [x] Save non-sensitive evidence and a runbook explaining first key registration, disable/revoke, preserved existing files and unsupported features.
- [x] Report exact candidate source, new images, tested behavior, production activation prerequisites and remaining items. Preserve all original documents and unknown resources.

## Completed candidate evidence — 2026-10-15

Verified implementation: `c24e9ba2ee9de5fe67b630e02875ba2e44fa3d01`; Portal: `92f4d50c7e61e60bbdf31121ab55b02134861e09`. This completion record changes documentation only.

Builder `go vet` and full serial-package Go/PostgreSQL regression passed: 41 packages, 5450 passing test events, zero failures. Eleven environment-dependent cases were skipped in the broad regression; the two personal FUSE cases subsequently passed with the explicit kernel integration gate enabled. New package combined integration coverage: personalstorage 81.4%, personalfs 96.0%, personalcsi 95.9% (87.9% total). Race, two-account SSH/SFTP, actual Driver/FuseMounter/kernel I/O, migration 58→59/repeat/legacy upgrades, disabled/enabled Helm rendering, and backend/CSI image builds passed. Portal Dockerfile.lint, 11 focused tests, dev build and seven Chromium component scenarios passed.

Builder evidence: `/tmp/rtp-personal-evidence-20261015/`; exact source: `/tmp/rtp-personal-final-20261015/`. Portal gates: `/tmp/rtp-personal-portal-gates.KShCuo/`; UI evidence: `/tmp/rtp-personal-storage-ui.0Y3H0i/evidence/`. Non-sensitive summaries, screenshots and bundles are delivered under the original checkout's `output/wellspiking-personal-20261015/`.

Both images are local verification artifacts; no registry push or cluster deployment occurred. Production remains Helm revision 274 and source `7af1eae22390cefc360113579c47a56139afb7f1`. Real Wellspiking account confinement, shared-root denial, host identity/network, public-key enrollment and actual debug-workspace acceptance remain activation prerequisites. Original uncommitted files were preserved. The isolated PostgreSQL container and network were removed after verification.
