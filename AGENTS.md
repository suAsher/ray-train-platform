# RayTrain contributor instructions

## Scope

This is the standalone GitHub project. Backend and Vue UI live in this repository and ship in one application image. Do not synchronize to private Git servers, build machines, or existing clusters. A code or documentation request does not authorize cluster deployment.

Read README.md and docs/ARCHITECTURE.md, then inspect actual source and git status. Preserve unrelated staged, unstaged, and untracked changes. Use a separate worktree for substantial work. If .codegraph exists, use CodeGraph before textual code searches; otherwise use rg without creating an index.

## Source and delivery

- backend/: Go API, controllers, CLI, database migrations, embedded web UI.
- frontend/: Vue application; build output goes into backend/webui/dist during the root Docker build.
- helm/, deploy/, images/, scripts/: portable installation and runtime artifacts.
- docs/: public architecture, storage, scheduling, deployment and development contracts.

Follow .agents/skills/release/SKILL.md for validation and GitHub delivery. Respect the user's execution constraints; GitHub Actions can validate candidates without a private builder. Never claim CI success or real GPU acceptance without checking results.

## Changes

Write meaningful regression tests for behavior changes. Run applicable Go, frontend, PostgreSQL and Helm checks. Migrations are append-only; preserve legacy upgrade fixtures and update complete-version assertions. Keep credentials, deployment-specific addresses and private evidence out of source. Do not silently enable preemption or alter existing workloads for testing.

The baseline storage provider is operator-provisioned PVC. TOS/FSX data spaces are optional and not a generic S3 backend. Preserve this distinction in docs and UI. GPU quota, compact placement, priority ordering and dedicated-node constraints have separate semantics; do not promise zero fragmentation.

Use conventional commit messages. GitHub is the sole repository delivery destination for this project. Pushing code does not authorize publishing an image or deploying a cluster beyond the scope requested by the user.
