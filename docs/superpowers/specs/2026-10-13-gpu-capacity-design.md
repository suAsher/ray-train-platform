# GPU capacity and compact placement

Approved scope: expose team quota, actual shared-pool capacity, request-shape fit and concrete queue explanations; place new training and debug workers compactly. Existing tasks must not move or restart. Publication requires separate approval.

## Contract

Authenticated `GET /api/v1/capacity` accepts `workerReplicas`, `gpusPerWorker`, `cpuPerWorker`, `memoryPerWorker`. Tenant and accelerator are server-resolved. Defaults: 1 worker, 1 GPU, 4 CPU, 16Gi memory. Return observation time, availability, own quota, pool total/allocated/unmaterialized-reserved/free GPUs, worker-shape fitting-node count and worker-slot count, and an explanatory reason. Node detail is SuperAdmin-only. This is a snapshot, not a reservation or a guarantee of immediate admission.

Pool accounting includes training, debug and all other nonterminal bound Pods, including terminating Pods, init containers and overhead. Kueue topology reservations supplement only occupancy not already represented by Pods. An unavailable or unresolvable observation is explicitly unknown, never a fabricated zero. CPU and memory constrain fit alongside GPU. Ready, schedulability, labels, taints and dedicated-node policy constrain eligibility.

Raw node/Pod/active-Workload observations are shared per effective selector and accelerator for five seconds, with single-flight collection. Tenant, dedicated-node policy, current-job exclusion, quota and request-shape calculations remain per request. Failed refreshes never return stale success. Observation time is collection start; cache age is bounded from collection completion. New training/debug placement allows five seconds for a cold observation; job diagnosis allows six seconds including its admission check.

Job diagnosis adds the same scheduling-capacity contract for early states, without replacing log evidence or misclassifying image/storage startup failures. Both Portal and CLI render this backend result. Existing quota accounting already includes debug workspaces and is not decremented by Pod usage again.

## Placement

For newly rendered workers, globally compact eligible partial nodes before opening empty nodes; same-team affinity breaks equal-capacity ties. Apply a soft preference only, retaining Kueue TAS admission and hard resource/node constraints. Debug workers use the same snapshot and preference. Do not modify existing RayJobs, workers, user resource specifications or enable preemption.

## UI

Show team remaining quota separately from pool free and reserved GPUs. Show fit for the current worker GPU/CPU/memory shape, timestamp and non-guarantee note. Failed refresh and stale replies must not display false zero or leak a previous team's data. Debounce shape changes; bounded refresh must stop on unmount/hidden page. Do not query the cluster once per job row. Clarify single-worker multi-GPU versus multi-worker placement.

## Validation

Observe RED on the builder before implementation, then unit/integration tests for accounting, reservation de-duplication, shape constraints, authorization, unavailable observations and compact ranking. Backend format/vet/full Go regression on isolated builder worktree with real PostgreSQL; Portal lint/contracts/dev build on builder. Review final candidates before requesting push/deploy approval. Production acceptance remains distinct from candidate tests.

References: https://kueue.sigs.k8s.io/docs/concepts/topology_aware_scheduling/ (capacity model); https://kubernetes.io/docs/concepts/scheduling-eviction/resource-bin-packing/ (compact-placement concepts). Inspected 2026-10-13; deployed Kueue version must be verified separately.
