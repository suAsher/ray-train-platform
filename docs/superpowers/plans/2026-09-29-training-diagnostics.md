# Training diagnostics implementation plan

> For agentic workers: use superpowers:subagent-driven-development and test-driven-development. The approved design is in the adjacent specs directory. Local operations are editing/review only; test commands below execute on the builder.

**Goal:** Show the same useful first-error evidence in CLI and Portal, bound confirmed runtime failure cleanup, and add Bash/Zsh completion.

**Architecture:** Shared read-only diagnosis API queries bounded lifecycle evidence from Loki. Clients render its contract; runtime supervision acts only on actual process exits. Completion uses existing authorized APIs with a short deadline.

**Tech Stack:** Go/Gin, Loki, Python/Ray subprocess supervision, Bash/Zsh, Vue Portal in its independent dev repository.

## 1. Diagnosis service and CLI

Files: new `backend/observability/diagnosis.go`, `backend/api/job_diagnosis.go`, tests alongside; extend Loki provider, jobs scoped routes; new `backend/spkrayjob/diagnose.go` and tests; small dispatch/help changes.

- [x] Add tests that call the authenticated diagnosis route and assert first NaN/KeyError/OOM evidence precedes later NCCL errors; verify inaccessible jobs never query Loki.
- [x] Run `go test ./observability ./api ./spkrayjob` against tests-only bundle; record expected missing-feature failures.
- [x] Implement bounded filtered query, deterministic evidence analysis, source context and coverage limitations. Add `spk-rayjob diagnose [connection/output flags] JOB_ID`, preserving existing status/logs behavior.
- [x] Re-run focused tests then `go vet ./...` and `go test -timeout=20m ./...` on builder.

## 2. Runtime supervision

Files: `images/workspace/raytrain-launch.py`, new `images/workspace/test_raytrain_launch.py`, existing `raytrain-launch.test.sh`; verify runtime Dockerfile COPY contracts.

- [x] Add synthetic process tests for failed worker plus hanging sibling, graceful TERM then KILL, successful exit, spawn errors, signal propagation and no log-text based termination.
- [x] Run `python3 -m unittest discover -s images/workspace -p test_raytrain_launch.py` on builder and observe expected failures.
- [x] Implement isolated process groups, nonblocking poll/stop actor operations, first-failure preservation and structured lifecycle events. Bound cleanup and protect unrelated processes.
- [x] Run Python tests and `bash images/workspace/raytrain-launch.test.sh` on builder; exercise dedicated CPU-only Ray integration if available.

## 3. Completion

Files: new `backend/spkrayjob/completion.go`, `completion_test.go`, small dispatch/help registration in `command.go`, CLI documentation.

- [x] Add tests for commands/flags/enums, filenames with spaces, authenticated task candidates, timeout, no token output, no cross-identity cache reuse, no API mutation.
- [x] Run `go test ./spkrayjob -run Completion` on builder and observe feature-missing failures.
- [x] Implement generated Bash/Zsh functions and internal completion protocol. Use no task-list cache: each dynamic completion uses the current identity/config with a 750 ms request deadline; avoid persistent credential-derived artifacts.
- [x] Validate generated scripts with actual Bash/Zsh on builder plus full CLI tests.

## 4. Portal

Files in the independent Portal dev checkout: `src/views/rayTrain/Job/JobDetail.vue`, focused diagnosis presentation helper and a RayTrain contract script.

- [x] Confirm current remote dev, isolate candidate and preserve existing master.
- [x] Add failing contract tests; add a manually refreshed diagnosis panel that consumes the shared endpoint, displays timestamp/source/context and labels uncertainty/unavailability.
- [x] Keep statusMessage/raw logs available and preserve task state. Do not render evidence as HTML.
- [x] On builder run Dockerfile.lint, existing RayTrain contracts and relevant build; no dev push.

## 5. Review and delivery

- [x] Independently review scope/contracts then correctness/security, resolve findings and retest changed behavior.
- [x] Record exact candidate SHAs and builder evidence, document runtime image rollout requirements and remaining acceptance limits.
- [x] Check original checkout retains all existing dirty docs/ZIPs; deliver local candidates without production changes.

Final source SHAs, builder evidence, the existing cross-package PostgreSQL lock-test isolation issue, and remaining production acceptance limits are recorded in `docs/TRAINING_DIAGNOSTICS.md`. Production rollout is outside this delivery.
