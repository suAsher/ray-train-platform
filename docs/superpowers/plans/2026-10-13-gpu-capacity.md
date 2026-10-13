# GPU capacity implementation plan

Design: `docs/superpowers/specs/2026-10-13-gpu-capacity-design.md`.

- [ ] Add RED tests in `backend/api/scheduling_capacity_test.go`, `backend/k8s/scheduling_capacity_test.go`, `backend/k8s/team_node_affinity_test.go` and Portal `src/views/rayTrain/schedulingCapacity.test.mjs`; observe failures in isolated builder containers.
- [ ] Implement `backend/domain/scheduling_capacity.go`, `backend/k8s/scheduling_capacity*.go` with Pod resource requests, node eligibility, Kueue reservation de-duplication and shape fit. API resolves authenticated tenant/accelerator/quota and redacts nodes for ordinary members.
- [ ] Update `backend/k8s/team_node_affinity.go` and initial-only reconciler options for global compact placement. Extend `backend/k8s/raycluster.go` and `backend/api/workspaces.go` so new debug workers share that preference.
- [ ] Extend `backend/observability/diagnosis.go`, `backend/api/job_diagnosis.go` and `backend/spkrayjob/diagnose.go` with early-state capacity explanation. Preserve existing log diagnosis and startup failure messages.
- [ ] Add Portal capacity loader/API/component; integrate quota overview, submit shape, debug page and job diagnosis. Cover delayed replies, team/shape switches, unavailable reads and lifecycle cleanup; add Docker lint contract.
- [ ] Commit reviewed candidates locally under platform identity; transfer Git bundles to builder, never overwrite formal source. Run builder formatting, `go vet ./...`, `go test -count=1 -p 1 -timeout=20m ./...` with isolated PostgreSQL. Run Portal Dockerfile.lint and Dockerfile.dev on exact candidate.
- [ ] Independent code/security review; fix and rerun relevant gates. Record source SHA and test evidence, known limits and distinct unverified production acceptance.
- [ ] Request explicit publication authorization for backend/CLI/Portal candidates and any scheduling configuration change. No production writes or user workload restarts as part of local implementation.
