# Unbounded Resumable Data-Space Upload Implementation Plan

> **Execution:** Follow the project release skill after every local verification step passes. The platform imposes no single-file product limit; the storage provider's multipart limits remain the physical boundary.

**Goal:** Let authenticated users upload `.pth` and other files larger than 5 GiB through the platform UI with progress, bounded-memory streaming, retries, and refresh-safe resume, without exposing TOS endpoints or credentials.

**Architecture:** Keep the existing single PUT relay for files up to 256 MiB. Larger files create a durable PostgreSQL multipart session. The browser hashes and uploads independently retryable slices to authenticated platform endpoints; the backend validates ownership and exact part sizes, streams each slice to TOS, records its ETag and SHA-256, and completes the object only after all parts are present. Expired sessions are safely aborted by a leader-elected cleanup worker.

**Tech stack:** Go 1.22, Gin, GORM/PostgreSQL, Volcengine TOS SDK v2, Vue 3, XMLHttpRequest, Web Crypto, Vitest, Helm/Kubernetes.

---

## Task 1: Define upload layout and lifecycle contracts

**Files:**
- Create: `backend/domain/data_space_upload.go`
- Create: `backend/domain/data_space_upload_test.go`
- Modify: `backend/domain/data_space.go`

1. Add failing table tests for zero-byte/small files, the 256 MiB boundary, >5 GiB files, the 10,000-part boundary, and integer overflow.
2. Add constants for the 256 MiB multipart threshold, 32 MiB ingress-safe preferred part size, 5 GiB maximum part size, 10,000 maximum parts, and 24-hour session TTL.
3. Implement a checked layout calculation that grows the part size as needed and never introduces a separate platform-wide whole-file cap.
4. Add immutable session/part domain records, lifecycle states, ownership checks, and expected-part-size calculation.
5. Run `env GOCACHE=/tmp/resumable-go-cache go test ./domain`.

## Task 2: Persist resumable sessions and completed parts

**Files:**
- Create: `backend/db/migrations/0029_data_space_multipart_uploads.up.sql`
- Modify: `backend/db/postgres_test.go`
- Create: `backend/repositories/data_space_uploads.go`
- Create: `backend/repositories/data_space_uploads_test.go`

1. Add a migration test asserting version 29 and the ownership, state, size, uniqueness, and foreign-key constraints.
2. Create `data_space_uploads` and `data_space_upload_parts`; keep raw TOS upload IDs server-only and cascade parts on session deletion.
3. Add repository tests for create-or-resume, per-owner isolation, idempotent part recording, completion locking, expiration extension, and cleanup claiming.
4. Implement transactional repository methods using row locks and compare-and-set state transitions.
5. Run `env GOCACHE=/tmp/resumable-go-cache go test ./db ./repositories`.

## Task 3: Add multipart storage capability

**Files:**
- Modify: `backend/objectstore/store.go`
- Modify: `backend/objectstore/tos_store.go`
- Modify: `backend/objectstore/tos.go`
- Modify: `backend/objectstore/tos_store_test.go`

1. Add failing store tests for create, upload part, complete, abort, invalid part numbers/sizes, and provider failures.
2. Add provider-neutral multipart request/result types to `DataSpaceStore`.
3. Add narrow TOS client adapter calls for `CreateMultipartUploadV2`, `UploadPartV2`, `CompleteMultipartUploadV2`, and `AbortMultipartUpload`.
4. Ensure every key is derived by `scopedTOSObjectKey`, every part streams from the request body, and provider errors map to `ErrUnavailable` without leaking internals.
5. Run `env GOCACHE=/tmp/resumable-go-cache go test ./objectstore`.

## Task 4: Implement authenticated multipart HTTP endpoints

**Files:**
- Modify: `backend/api/jobs.go`
- Modify: `backend/api/data_spaces.go`
- Create: `backend/api/data_space_multipart.go`
- Create: `backend/api/data_space_multipart_test.go`
- Modify existing data-space upload tests as required.

1. Add failing handler tests for create/resume responses, wrong owner/space/path, exact part length, SHA-256 mismatch, duplicate retry, incomplete completion, successful completion, and abort.
2. Extend upload-ticket creation to return `mode: single` for <=256 MiB and create/resume a durable session for larger files.
3. Add `PUT /data-spaces/:id/uploads/:session/parts/:partNumber`, streaming through `io.TeeReader` into TOS while calculating SHA-256 and enforcing the exact expected length.
4. Add `POST .../:session/complete` and `DELETE .../:session`, with idempotent terminal-state behavior.
5. Preserve the existing single-upload endpoint and cap only that request at the multipart threshold.
6. Return stable, user-safe error codes; never expose root prefixes, object keys, upload IDs, or storage errors.
7. Run `env GOCACHE=/tmp/resumable-go-cache go test ./api`.

## Task 5: Abort expired multipart sessions

**Files:**
- Create: `backend/api/data_space_upload_cleanup.go`
- Create: `backend/api/data_space_upload_cleanup_test.go`
- Modify: `backend/main.go`

1. Add failing tests for expiration selection, successful abort, retry after provider failure, and cancellation.
2. Implement a bounded cleanup pass that claims expired active sessions, aborts TOS multipart uploads, and marks them aborted only after the provider confirms.
3. Run it under the existing Kubernetes leader-election facility so multiple backend replicas cannot race cleanup.
4. Run `env GOCACHE=/tmp/resumable-go-cache go test ./api ./...`.

## Task 6: Build the browser multipart transport

**Files:**
- Modify: `frontend/src/api/dataSpaces.js`
- Create: `frontend/src/api/dataSpaceMultipartUpload.js`
- Create: `frontend/src/api/dataSpaceMultipartUpload.test.js`
- Modify: `frontend/src/api/dataSpaceUpload.js`

1. Add failing Vitest cases for part planning, three-way concurrency, aggregate progress, bounded retry/backoff, completed-part verification, complete, and cancellation.
2. Add authenticated create/part/complete/abort API helpers.
3. Implement slice hashing with Web Crypto, one slice at a time, so memory is bounded by part size and no whole-file buffer is created.
4. Upload at most three parts concurrently with XHR progress; retry transient/network/5xx failures, but surface auth and validation failures immediately.
5. Verify server-reported completed-part hashes against the reselected local file before skipping them.
6. Run `npm test -- --run src/api/dataSpaceMultipartUpload.test.js`.

## Task 7: Integrate resumable progress into Data Explorer

**Files:**
- Modify: `frontend/src/views/DataCache/index.vue`
- Modify: relevant `frontend/src/**/*.test.js` files.

1. Add failing UI/controller tests for dispatching single vs multipart, total progress, retry text, and resumable failure state.
2. Dispatch from the returned ticket mode while retaining folder multi-file behavior.
3. Display overall bytes, current part count, and retry state without treating a transient failed part as a failed whole file.
4. Persist only the opaque session ID and safe file identity metadata in local storage; require the user to reselect the file after refresh because browsers cannot retain file handles.
5. Run `npm test` and `npm run build`.

## Task 8: Verify and review the complete change

1. Run `gofmt` on changed Go files.
2. Run `env GOCACHE=/tmp/resumable-go-cache go test ./...` and `env GOCACHE=/tmp/resumable-go-cache go build ./...`.
3. Run `npm test` and `npm run build` in `frontend/`.
4. Inspect `git diff --check`, migration safety, input validation, authorization, resource bounds, and secret leakage.
5. Confirm the main worktree's unrelated untracked files remain untouched.
6. Commit with conventional commits and merge the verified branch into `main` without overwriting user work.

## Task 9: Build, deploy, and production-verify

1. Confirm local `main`, `origin/main`, and the build-host checkout base before transfer.
2. Transfer the exact commit with a git bundle to `/opt/guofeng/vke-cluster/ray-platform-main`.
3. Build only backend and frontend images with an immutable release tag; verify each image digest and architecture.
4. Back up live Helm values. Render with `--reuse-values`, verify migration 29 is present, ensure no `idc-readonly-sources.yaml` downgrade, and diff the exact manifests.
5. Deploy atomically and wait for backend/frontend rollout readiness. Do not restart or delete RayJobs, RayClusters, or training pods.
6. Verify health/readiness, Helm revision, live image digests, database migration 29, and unchanged active training workloads.
7. Exercise through the public platform route:
   - a small single PUT upload;
   - a multipart test object using several small HTTP parts against the same production multipart code path;
   - retry one completed part and verify idempotency;
   - complete and download/list the resulting object;
   - create and abort a second session;
   - confirm no TOS URL, bucket, key, upload ID, or credential appears in browser-visible responses.
8. Report the exact commit, image tags/digests, Helm revision, tests, production evidence, and user recovery steps.
