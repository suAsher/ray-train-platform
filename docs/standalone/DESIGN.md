# Standalone RayTrain distribution

This change converts the repository into an independently deployable training platform. It is a new-install distribution, not an instruction to upgrade an existing company deployment. Existing Git history is retained; company-specific deployment records are removed from the current distribution.

## Decisions

- Ship the Vue console and Go API in one image and one HTTP origin. The repository owns both; no external Portal repository, ingress bridge, or company SSO is required.
- Keep Kubernetes, KubeRay, Kueue and PostgreSQL as explicit prerequisites. Local accounts and PATs provide the default authentication path. OIDC remains optional, with no preset issuer.
- Use administrator-registered, tenant-local Kubernetes PVC assets for portable training inputs, checkpoints and outputs. Users choose asset IDs, not arbitrary PVCs or host paths. Existing optional TOS/FSX integration is explicitly separate from the portable path; do not claim S3 compatibility without implementing and testing it.
- Remove company function-warehouse integration, scheduled storage synchronization and IDC/WellSpiking mount entry points. No company endpoints or fixed node addresses belong in install defaults or runtime help.
- Keep quota admission, topology-aware placement, team affinity and explicit priority policy. Document the boundaries: soft affinity cannot guarantee compaction, no live migration of training, and preemption is opt-in.
- Publish only to the existing GitHub repository. Do not access the builder, GitLab, or a running cluster. Use GitHub-hosted CI for compilation, tests, PostgreSQL integration, Helm rendering and image build checks; main receives validated changes.

## Alternatives considered

A same-repository pair of frontend/backend deployments would preserve the old chart but retain separate versioning and routing work. A full rewrite would discard tested authorization and lifecycle behavior. A combined artifact reuses the existing frontend and control plane while removing the external delivery dependency.

## Validation and limits

Test authenticated API boundaries, removed integration routes, PVC mount validation, SPA routing versus API 404s, frontend production build, chart installation rendering and database migrations. Cluster smoke tests may run only in an ephemeral CI-created cluster. No physical GPU performance or real production-cluster acceptance is claimed by source tests. A maintainer must validate device plugin, GPU drivers, storage and topology in each target cluster.
