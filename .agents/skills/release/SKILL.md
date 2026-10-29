---
name: release
description: RayTrain standalone GitHub development, validation, image build and Helm delivery workflow.
---

# Standalone project delivery

This skill applies to the public, self-contained RayTrain repository. The Vue UI and Go API ship together through the root Dockerfile. GitHub is the sole repository delivery destination. There is no private builder, private Portal repository, or implied live cluster deployment.

User instructions override this workflow. If the user excludes local builds, use approved CI rather than silently using a historical build host. Do not require a production cluster for source-only changes.

## Inspect

Read git status and relevant docs before changing source. Preserve unrelated changes. Use a worktree when needed. Read [repository map](references/repository-map.md), [acceptance](references/acceptance.md), or [diagnostics](references/diagnostics.md) as appropriate.

## Validate

Use GitHub Actions or a developer-selected environment for Go formatting/vet/regression, frontend tests/build, PostgreSQL integration tests and Helm/runtime contracts. Follow docs/DEVELOPMENT.md. Tests skipped for missing external services are not passing evidence. Migration changes require real PostgreSQL fresh-install and upgrade tests.

The root Dockerfile builds Vue and embeds it in the Go binary. Build helper/runtime images only when required, with their documented contexts. Do not invent published image digests. A GPU training acceptance needs separately authorized target resources; never stop unrelated workloads.

## GitHub delivery

Review diff and secret-sensitive files, commit to a candidate branch, push only to the requested GitHub repository, and inspect CI results before promoting main. Do not synchronize another remote or build machine. Never force push over someone else's work. Record source SHA and check results separately from image digests.

Version tags can trigger the GHCR publication job after CI passes. Publishing a tag/image is a separate action from a source branch push; perform it only when within the user's authorization. Existing workflow publication is not proof that an image already exists.

## Cluster installation, only when requested

Follow docs/DEPLOYMENT.md using the user's chosen context. Inspect rendered manifests and RBAC, use deployment-specific Secrets and values, and preserve data. Back up databases before upgrades; Helm rollback does not revert migrations. Report installed versions and real task evidence without claiming CI alone proves GPU behavior.

## Report

Distinguish source changes, CI validation, image publication and cluster installation. State any unverified capability. Keep credentials and private environment evidence out of commits and user-visible logs.
