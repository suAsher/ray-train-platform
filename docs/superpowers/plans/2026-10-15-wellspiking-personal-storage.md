# Wellspiking Personal Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Follow the ownership boundaries below and preserve concurrent changes.

**Goal:** Provide owner-only Wellspiking personal file access and a default writable debug-worker mount with unique per-user keys.

**Architecture:** Backend-owned stable user bindings and Secrets feed a fixed-origin SFTP adapter. A separate CSI node/FUSE component exposes only that user's personal root; Portal consumes explicit capabilities and never assumes training eligibility.

**Tech Stack:** Go/Gin/GORM/PostgreSQL, SSH/SFTP, CSI/gRPC/FUSE, Vue/Element Plus in FDE dev, Kubernetes retained static PV/PVC.

## Task 1 — Personal binding and file-store boundary

Files: backend/personalstorage/{types,policy,service,sftp,vault}.go and corresponding tests; backend/repositories/personal_storage.go; backend/db/migrations/0059_personal_storage.up.sql; migration version contracts. Own only this package, repository and migration work. Repository identity is stable user ID, not tenant. Define shared exported interfaces before consumers integrate.

- [ ] Write regression tests for independent keys, stable owner binding, wrong owner/username, excluded shared roots, absolute/traversal/symlink paths, host-key rejection and bounded upload.
- [ ] Commit only new tests into the isolated candidate and send a Git bundle to builder; observe missing-feature RED with `go test ./personalstorage ./repositories -run PersonalStorage`.
- [ ] Implement the personal-only service and fixed-endpoint SFTP adapter; no remote root/host/user override from HTTP.
- [ ] Run focused GREEN plus real two-user SFTP fixture. Verify migration 59 new/repeat/upgrade against isolated PostgreSQL and preserve versioned fixtures.

## Task 2 — CSI and FUSE mount

Files: backend/personalfs/, backend/personalcsi/, backend/cmd/personal-storage-csi/, images/personal-storage-csi/, helm/ray-train-platform/templates/personal-storage-csi.yaml. Coordinate dependencies through Task 1 owner; do not edit API or workspace renderer.

- [ ] Write tests for lookup/readdir/read/write/mkdir and exclusion of other roots/links; NodePublish rejects mismatched owner/binding/generation and paths outside a kubelet publish target.
- [ ] Observe RED on the builder before implementing.
- [ ] Implement identity/probe/node CSI calls and per-volume FUSE instances. Do not expose a network CSI endpoint or mount credential files into user Pods. Return actual remote errors and close connections/unmount on cancellation.
- [ ] Exercise two real SFTP accounts from isolated FUSE mounts on builder. `ls`, bidirectional writes, unmount/remount and wrong credentials must give real evidence. Test daemon restart/target ownership and record unsupported semantics explicitly.

## Task 3 — API, config and workspace integration

Files: backend/api/personal_storage*.go, backend/api/data_spaces.go, backend/api/workspaces.go, backend/api/jobs.go, backend/config/personal_storage.go, backend/main.go, backend/k8s/personal_storage.go, backend/k8s/raycluster.go, backend/domain/data_space.go, backend/domain/data_location.go, helm/ray-train-platform/{values.yaml,templates/backend-deployment.yaml}.

- [ ] Test interactive-only owner derivation, disabled feature, no target-user input, space capabilities and all existing training entry points rejecting the new space.
- [ ] Test that only the debug worker receives the PVC; no Secret, FUSE device, capability or privilege is added to head/worker; unconnected users retain existing behavior.
- [ ] Observe missing behavior RED; implement explicit provider dispatch and mount readiness checks without weakening FSX/NFS contracts.
- [ ] Run API/RBAC/render regression and full Go gates with PostgreSQL on builder. Feature stays disabled in defaults; enabling requires pinned driver image and known host data.

## Task 4 — FDE Portal

Files under src/views/rayTrain/: DataCache, Devcenter, api/personalStorage.js, dataSpaceActions.js/dataSpaceReadiness.js and narrow tests. Read current FDE AGENTS, development-spec and ui-style-guide. Use fresh FDE dev; never publish the old backend/frontend directory.

- [ ] Write tests for per-user connection state, public-key-only display, capability-driven upload/browse, no training/preview/download buttons and rejection of stale team/user responses.
- [ ] Observe RED in builder; implement one-time enrollment/verify and mount status using returned paths rather than hardcoded IDC labels.
- [ ] Run focused tests, Dockerfile.lint and Dockerfile.dev on the exact candidate. No push or deployment in this task without a concrete reviewed candidate and current authorization.

## Task 5 — Review, isolated evidence and delivery

- [ ] Independent code/security review: upstream account isolation is the privacy boundary; filtering is not a substitute. Review Secrets, public responses, host keys, owner CAS, symlink semantics, cancellation and CSI node privilege.
- [ ] Full gofmt/vet/Go/PostgreSQL gates on builder; meaningful coverage of new behavior; report skipped production cases separately.
- [ ] Save non-sensitive evidence and a runbook explaining first key registration, disable/revoke, preserved existing files and unsupported features.
- [ ] Report exact candidate source, new images, tested behavior, production activation prerequisites and remaining items. Preserve all original documents and unknown resources.
