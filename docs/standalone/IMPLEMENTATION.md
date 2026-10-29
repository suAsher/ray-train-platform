# Standalone implementation plan

Goal: a self-contained GitHub distribution with portable installation defaults and documented scheduling behavior.

- Backend: remove company integrations and callback/controller wiring; reject removed configuration; add generic PVC asset validation and tenant-isolation tests. Preserve migration history.
- Web: remove company UI and links; support PVC catalogue selection; embed the built console and serve it from the API origin. Test static routes, SPA routes, and API misses separately.
- Delivery: replace company profiles with generic Helm configuration, one application deployment, bootstrap secret instructions, public image sources and GitHub-only CI. Render default and example configurations.
- Documentation: replace production snapshots with project README, architecture, installation, configuration, development, security and GPU scheduling guides. Remove retired company operational scripts, manifests and data examples from the distribution.
- Verification: push candidate branch to GitHub; run frontend tests/build, Go format/vet/regression, real PostgreSQL migrations, runtime tests and Helm contracts. Install the unified image into a disposable CI cluster and verify local accounts, scoped PATs and persistence. Resolve failures, review the complete diff and then fast-forward GitHub main only.

This document records the implementation scope, not a live test-status checklist. Review evidence and execution status are recorded in [the standalone pull request](https://github.com/suAsher/ray-train-platform/pull/1) and its GitHub Actions checks.

Ownership: backend/API integration changes, frontend/static server, Helm/CI delivery and documentation are separate work areas. All contributors preserve other contributors' changes. The original working directory is left untouched.
