# Acceptance

Use docs/DEPLOYMENT.md for a fresh installation and docs/STORAGE.md for the selected provider. Do not reuse real-user jobs for testing.

Source validation includes Go, frontend, real PostgreSQL and Helm checks in GitHub Actions. Runtime acceptance separately verifies a dedicated test team's identity, quota, image, fixed Git commit, authorized PVC input and writable output.

For GPU placement, inspect Pod NODE and requests alongside Kueue admission/topologyAssignment. Exercise small jobs, full-node shapes and dedicated-node exclusions. Priority ordering is distinct from preemption; leave preemption disabled unless a safe recovery policy has been independently verified.

Basic PVC mode currently uses Web submission; CLI PVC asset parameters are not implemented. working-dir and native Ray uploads require configured object-storage archive support. Do not report these paths working from UI build success alone.

Clean only test objects explicitly created for the acceptance and authorized for removal. Record source SHA, image digest, cluster component versions and outcomes; never include credentials.
