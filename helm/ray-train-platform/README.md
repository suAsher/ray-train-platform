# RayTrain Helm chart

The chart runs one application image containing the Go API and Vue UI, on port 8080.
It does not install cluster operators or provision GPU nodes.

Start with [the project README](../../README.md) and
[quickstart values](../../deploy/profiles/quickstart.yaml).
Supply your own built image references, workspace and source-materializer digests,
PostgreSQL and authentication Secrets, and install KubeRay and Kueue before training.

The release namespace is taken from `helm --namespace`; fixed names mean one
RayTrain installation per cluster. The control plane creates tenant namespaces
and requires the ClusterRole documented in `templates/rbac.yaml`.

Defaults use local accounts and PATs. OIDC, TOS/FSX data spaces, MLflow,
observability endpoints, topology-aware admission and preemption are optional.
No external Portal, company storage mount, or data synchronization service is installed.

Use `scripts/test-delivery-render.sh` to verify the portable rendering contract.
`deploy/profiles/production.yaml.example` requires an external PostgreSQL service
and immutable control-plane image digest; it is an overlay, not a complete install.
