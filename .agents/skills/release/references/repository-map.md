# Repository map

- `backend/`: Go API, authentication, job reconciler, Kubernetes integration, CLI and database migrations.
- `backend/webui/`: embedded application UI; root Dockerfile supplies built Vue assets.
- `frontend/`: authoritative Vue UI in this repository.
- `helm/ray-train-platform/`: portable control plane chart.
- `deploy/profiles/quickstart.yaml`: development/evaluation example with standalone PostgreSQL.
- `deploy/profiles/production.yaml.example`: external PostgreSQL and immutable-image production overlay.
- `images/`: source materializer, training and workspace runtimes; use correct Docker build contexts.
- `.github/workflows/ci.yml`: source validation and version-tag publication.
- `docs/`: public deployment, architecture, scheduling, storage and development references.

No external Portal repository or private CI is required. Read actual source/configuration before describing behavior. Old Git history is historical evidence, not an active environment contract.
