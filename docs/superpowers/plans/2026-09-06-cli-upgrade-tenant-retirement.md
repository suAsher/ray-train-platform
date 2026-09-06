# CLI Upgrade and Tenant Retirement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox syntax for tracking.

**Goal:** Implement the approved CLI/update, onboarding and team-retirement design without production mutations.

**Architecture:** Three independently owned modules share a tested integration boundary. CLI release metadata lives with downloadable binaries; team lifecycle is enforced in backend transactions and authentication, while documentation preserves administrator edits.

**Tech Stack:** Go/Gin/GORM/PostgreSQL, Vue/Node tests, shell/Python ops tests.

## 1. CLI update protocol and executable replacement

Files: `backend/spkrayjob/updates*.go`, `backend/spkrayjob/command.go`, `backend/spkrayjob/client.go`, `backend/Dockerfile.spk-rayjob`, release metadata build helper and tests.

- [x] Write red tests for numeric release comparison, unknown versions, notice-only newer version, minimum incompatible version, no stdout pollution, timeouts and bad metadata.
- [x] Implement release manifest schema: `schemaVersion`, `latestVersion`, `minimumVersion`, `releaseNotes`, `artifacts[{os,arch,filename,sha256,size}]`. Serve `/downloads/spk-rayjob/release.json`; same-origin fixed filenames, no credentials on downloads.
- [x] Add `upgrade` dispatch and login/submit checks. Unsupported systems fail explicitly; downloads use bounded HTTPS, checksum and platform checks before replacing current executable.
- [x] Test failure preservation, write permissions, redirects, invalid checksums, concurrency and Windows helper quoting/rollback; cross-build all supported targets.
- [x] Root integrates backend version header compatibility guard and UI release display against the shared manifest contract.

Commands: `cd backend && go test ./spkrayjob && go build ./...`; expect all tests/build pass. UI display tests cover invalid/offline/latest/minimum versions.

### Development verification record (2026-09-06; not a release)

- Integrated backend `go test ./...` and `go build ./...` passed after final review fixes.
- Frontend full suite: 342 passed; build passed (existing chunk-size warning).
- NVMe registration operations contract tests passed using mocked kubectl/ssh.
- Local browser with mock APIs: latest release and upgrade command displayed;
  wrong team ID blocked confirmation; exact confirmation hid the retired team;
  TenantAdmin had no retirement action. No production API was called.
- CLI worker/root verification covered a real Unix process upgrade, three
  cross-compilation targets, executable-format checks, non-overwriting Unix
  rollback backups and Windows `.previous` backup refusal.
- PostgreSQL integration tests are skipped without POSTGRES_TEST_DSN. Native
  Windows executable replacement has not been run. Neither is production-verified.
- Release verification: CLI package statement coverage is now 80.2%.
- PostgreSQL 16 isolated verification passed: complete migrations, advisory
  exclusion, identity/user/session/PAT retirement concurrency, and active-job
  writes plus retired child/parent ownership guards. No production DB was used.
- Windows self-replacement is release-gated off pending native acceptance;
  its CLI remains installable manually from the external-submit page.
- User explicitly permits rolling platform components while training continues;
  no existing RayJob/RayCluster/Pod may be changed or restarted.
- This change intentionally preserves the existing RayJob retention behavior;
  repairing its unrelated configuration parsing must be a separate change.
- No push, deployment, team retirement, node registration or training submission.
- Final review fixes include parent-owner fences for checkpoint/event/cache/
  upload-part rows, an actor fence during retirement, and short lifecycle checks
  before read responses (no long-lived DB connection while streaming).
- Final rerun: backend all packages and build passed; frontend 342 tests and
  build passed; migration SQL still requires execution against real PostgreSQL.

## 2. Node registration and user-facing instructions

Files: `ops/storage/nvme-cache/register-node.sh`, registration helper/tests, `docs/OPERATIONS_GUIDE.md`, `docs/BUILD_AND_DEPLOY.md`, `frontend/src/help/*`, `backend/helpdocs/seed.json`.

- [x] Red tests: adding third/fourth node retains every existing node and emits separate single-disk maps; active/non-cordon node, incorrect mounts, conflicting names and duplicate output fail closed.
- [x] Replace hardcoded node lists with explicit existing data1/data2 config inputs. Keep review-only output; do not apply Helm, label/uncordon or format disks.
- [x] Add separate UI topics for CLI upgrade, two complete submission methods and node onboarding; retain existing editable documents instead of overwriting them. Update legacy contradictory docs.
- [x] Run shell/Python registration tests and `cd frontend && npm test && npm run build`; verify published seed contains new topics with no secret literals or command generator.

## 3. Tenant lifecycle backend and administration UI

Files: `backend/repositories/tenant_retirement*.go`, `identity.go`, `backend/api/tenant_retirement*.go`, authentication/writer integration, migration0034 and db version test; `frontend/src/components/admin/TenantPanel.vue` and API/helpers/tests.

- [x] Red tests for SuperAdmin-only interactive mutation, protected current/bootstrap tenants, retired ID preservation, active work blocking and history preservation.
- [x] Add persistent retirement timestamp/actor; implement read-only dependency preflight and transactional retirement with refreshed checks, credential revocation, and no storage/Kubernetes deletion.
- [x] Enforce retirement on authentication/EnsureIdentity and team writes; serialize retirement with relevant writers, reject unknown/live Kubernetes state and preserve read-only administrator historical access.
- [x] Add UI preflight counts/blockers, typed team-ID confirmation, retired filter, audit display. No hard delete or restore action.
- [ ] Finish real PostgreSQL concurrent submit/upload/identity retirement verification. Local stale-preflight, identity, repeat-retirement and permission tests pass, but do not substitute for this gate.

## 4. Integration and acceptance

- [x] Review approved design coverage independently, then code/security quality; fix issues and re-review.
- [x] `cd backend && gofmt -l . && go test ./... && go build ./...`.
- [x] `cd frontend && npm test && npm run build`.
- [x] Local mocked browser checks for team retirement confirmation/roles and release information. Actual PostgreSQL concurrency and native Windows upgrade require explicit environment evidence; report any not executed.
- [x] Record results and remaining release-only validation. Do not push, deploy, retire test or consume GPU resources during this development task.
