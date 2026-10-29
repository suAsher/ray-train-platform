# Diagnostics

Start from the user's requested scope. Repository-only analysis does not authorize cluster access or mutation. For an authorized cluster, verify context and namespace before commands.

- Login: separate local account credentials, PATs and optional OIDC claims. Platform membership determines authorization.
- Submission: inspect API validation, allowed image/Git sources, team quota and stored desired state.
- Queueing: inspect Kueue Workload admission, Flavor/topology support and available Worker shape. A team quota is not free physical capacity.
- Pod startup: separate scheduling, image pull, PVC mount and container process failures.
- Storage: PVC assets require Bound claims in tenant namespaces. Optional TOS/FSX paths require explicit CSI/identity configuration; no automatic IDC mounts exist.
- Logs: verify configured Loki and current Pod logs separately. Missing historical logs do not prove the training never ran.
- Upgrade: compare source SHA, application image digest and Helm manifests; they are different version identifiers.

Use bounded read-only observations first. Never dump Secrets or kill an unknown process. Do not restart existing workloads as a diagnostic shortcut.
