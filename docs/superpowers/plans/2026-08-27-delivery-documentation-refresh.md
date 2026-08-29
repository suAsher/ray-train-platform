# Delivery Documentation Refresh Implementation Plan

> **Goal:** Consolidate the repository into one maintainable documentation path that is grounded in the current source, production profile, and read-only cluster inventory.

## Scope

- [x] Verify the local checkout, build-machine checkout, and GitHub `main` commit.
- [x] Inventory the platform, storage, scheduling, observability, and MLflow components.
- [x] Define the documentation hierarchy and source-of-truth rules.
- [x] Add a complete newcomer and handover guide.
- [x] Replace the production `rsync` flow with a Git-first release workflow.
- [x] Correct stale NVMe cache statements across active documents.
- [x] Add MLflow explicitly to the production architecture diagram.
- [x] Validate documentation links, examples, rendering, and repository tests.

## Documentation hierarchy

1. `README.md`: public product and architecture overview.
2. `docs/README.md`: the only documentation index.
3. `docs/HANDOVER_GUIDE.md`: first document for a new maintainer.
4. `docs/BUILD_AND_DEPLOY.md`: build, release, upgrade, and rollback commands.
5. `docs/OPERATIONS_GUIDE.md`: routine inspection and incident runbooks.
6. `docs/ARCHITECTURE.md`: design boundaries and data/control flows.
7. User and administrator guides: role-specific workflows.
8. `docs/archive/` and `docs/superpowers/`: historical records, not operational truth.

## Acceptance

- [x] Active documentation contains no contradictory NVMe enablement statement.
- [x] Production release instructions require a clean, pushed Git commit.
- [x] A new maintainer can identify all releases, namespaces, persistent stores, and rollback paths from one guide.
- [x] All Markdown links and repository documentation tests pass.
- [x] The final diff contains no credentials, generated backups, or unrelated source changes.
