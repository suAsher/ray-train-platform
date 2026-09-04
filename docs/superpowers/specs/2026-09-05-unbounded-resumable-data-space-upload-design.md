# Unbounded resumable data-space uploads

Date: 2026-09-05

## Goal

Let an authenticated user upload a checkpoint or other file larger than 5 GiB
through the existing personal data-space UI with visible aggregate progress,
bounded concurrency, per-part retries, and resume after a browser refresh or
restart.

The platform must not impose an independent whole-file size limit. The only
whole-object ceiling is the backing TOS multipart contract: at most 10,000
parts, no part larger than 5 GiB, and therefore an effective object maximum of
about 48.8 TiB. Files that cannot be represented within that TOS contract are
rejected with a message that identifies the storage limit rather than calling
it a platform upload limit.

## Existing constraints

- Browsers reach the platform but cannot reach the VPC-internal TOS endpoint.
  Every upload part therefore remains an authenticated same-origin request to
  the platform and is relayed to TOS by the backend.
- A normal TOS PutObject is limited to 5 GiB. Larger files require
  CreateMultipartUpload, UploadPart, CompleteMultipartUpload, and abort support.
- The frontend Nginx already streams `/api/` request bodies and permits a body
  up to 5 GiB. A multipart request must never contain more than one TOS part,
  so this per-request boundary remains valid.
- Data-space roots and TOS object keys are server-derived security boundaries.
  The browser must never receive a TOS endpoint, credential, internal root,
  object key, or raw TOS upload ID.
- The backend has multiple replicas. Multipart state must survive a replica
  change or process restart and cannot live only in memory.

## Chosen approach

Use browser-managed parts, platform-relayed part requests, PostgreSQL-backed
upload sessions, and TOS Multipart Upload.

Alternatives rejected:

1. One arbitrarily large browser PUT with backend-side splitting cannot resume
   after a connection failure and remains exposed to the load balancer's
   request timeout.
2. Browser-to-TOS presigned multipart upload would provide the best throughput,
   but the current TOS endpoint is private. Making it browser-reachable would
   expand the network and credential boundary and is outside this feature.

## Upload modes

The existing create-upload operation chooses one of two modes and returns an
explicit `mode` field:

- `single`: files no larger than 256 MiB use the existing streaming PUT.
- `multipart`: files larger than 256 MiB use the resumable multipart protocol.

Using multipart below the 5 GiB PutObject limit gives ordinary large
checkpoints the same retry and resume behavior as files above 5 GiB. The
256 MiB threshold is an implementation constant, not a maximum file size.

## Dynamic part sizing

The backend is authoritative for part sizing. It calculates:

1. Start at 32 MiB. This stays below the production Volcengine ALB listener's
   default 60 MiB request-body limit, which otherwise returns 413 before the
   request reaches the frontend or backend.
2. If `ceil(size / 32 MiB)` exceeds 10,000, choose the smallest whole-MiB part
   size for which `ceil(size / partSize) <= 10,000`.
3. Reject only when the required part size exceeds 5 GiB or an input cannot be
   represented safely as a signed 64-bit byte count.

The production ALB listener must bind a customized configuration with
`client_max_body_size 5120M` and `proxy_request_buffering off` before dynamic
parts are allowed to grow beyond the ALB default 60 MiB. The Helm value
`albInstance.https.customizedCfgID` owns that binding. Without the cloud-side
configuration, 32 MiB ordinary parts still support practical files above 5 GiB
but the ALB will reject an individual dynamically grown part above 60 MiB.

The response includes `partSizeBytes`, `totalParts`, and exact expected sizes
for the final part. The frontend does not invent or override these values.

The frontend uploads at most three parts concurrently. It reads and hashes a
file incrementally in smaller local chunks; it never materializes a multi-GiB
part as one JavaScript ArrayBuffer. `File.slice()` supplies the request body.

## Persistent model

Add a PostgreSQL upload-session table with these logical fields:

- platform session ID: random, opaque, and safe to return to its owner;
- tenant ID, authenticated subject, logical data-space ID;
- server-derived root prefix and normalized relative path;
- content type, total size, part size, total parts;
- browser-generated resume key;
- raw TOS upload ID, stored only server-side;
- state: `ACTIVE`, `COMPLETING`, `COMPLETED`, `ABORTING`, `ABORTED`, or `FAILED`;
- completion identity and timestamps;
- expiry timestamp, initially 24 hours after the latest successful activity.

Add a child part table keyed by session and part number:

- exact byte size;
- lowercase SHA-256 digest;
- TOS ETag;
- completion timestamp.

Only one non-terminal session may target the same owner, space, and relative
path. A new, different file targeting that path receives a conflict response;
the UI can abort the old session before starting the replacement. Repeating a
create request with the same resume key returns the active session and its
completed parts.

## API contract

### Create or resume

`POST /api/v1/data-spaces/:space/uploads`

The existing request retains `path`, `contentType`, and `sizeBytes`, and adds an
optional opaque `resumeKey`. A multipart response contains only platform-level
information:

```json
{
  "mode": "multipart",
  "sessionId": "upload-opaque-id",
  "partSizeBytes": 33554432,
  "totalParts": 129,
  "completedParts": [
    {"partNumber": 1, "sizeBytes": 33554432, "sha256": "..."}
  ],
  "expiresAt": "2026-09-06T00:00:00Z"
}
```

The single-upload response keeps its existing URL and headers and adds
`"mode": "single"` for explicit dispatch.

### Upload one part

`PUT /api/v1/data-spaces/:space/uploads/:session/parts/:partNumber`

Required headers include `Content-Length`, `Content-Type:
application/octet-stream`, and `X-Content-SHA256`. The backend verifies:

- current caller owns the session and still has write access to the same space;
- the current authorized root equals the session's stored root;
- session is active and unexpired;
- part number and content length exactly match the authoritative layout;
- the streamed SHA-256 equals the declared digest.

The backend streams the request through an incremental SHA-256 hasher into TOS
UploadPart. It records the ETag only after TOS succeeds and the digest matches.
Re-uploading the same part number overwrites that TOS part. A repeated request
whose recorded size and digest already match is idempotently successful.

### Complete

`POST /api/v1/data-spaces/:space/uploads/:session/complete`

Completion takes a database row lock, verifies that every expected part exists
with the exact size and total byte count, changes state to `COMPLETING`, and
calls TOS CompleteMultipartUpload with the ordered part number and ETag list.
The final TOS object carries platform session metadata so a retry can reconcile
a TOS success followed by a database failure. A successful completion becomes
`COMPLETED` and returns the logical relative path.

Completion is idempotent. If TOS reports that the upload ID is already gone,
the backend HEADs the target and accepts it only when the session metadata and
object size match.

### Abort

`DELETE /api/v1/data-spaces/:space/uploads/:session`

Abort is owner-authorized and idempotent. It transitions the session through
`ABORTING`, calls TOS AbortMultipartUpload, and ends in `ABORTED`. A completed
object is never deleted by this endpoint.

## Frontend behavior

The browser creates and stores a random resume key in local storage, indexed by
the logical space, relative path, size, and last-modified time. It stores no
credentials or storage coordinates.

For multipart mode it:

1. Reconciles server-reported completed parts with the reselected local file by
   recalculating their SHA-256 digests. A mismatch schedules that part for
   overwrite rather than trusting stale bytes.
2. Hashes pending parts incrementally without blocking the main UI thread.
3. Uploads at most three parts concurrently.
4. Retries network errors, 408, 429, and 5xx responses with bounded exponential
   backoff. Other 4xx responses stop the session and display the server reason.
5. Computes aggregate progress from durable completed bytes plus in-flight byte
   counters without double-counting retries.
6. Displays bytes sent, percentage, current throughput, estimated remaining
   time, completed/total parts, and retry state.
7. Calls complete only after every part succeeds. It removes local resume state
   after the server confirms completion or abort.

After a refresh or browser restart the browser cannot reopen a local file
without user consent. The UI asks the user to select the same file again, then
reuses the resume key, verifies completed part digests, and uploads only missing
or mismatched parts.

Folder uploads continue to process files independently. One failed multipart
file does not repeat already successful sibling files.

## Failure and cleanup behavior

- A failed part never becomes completed in PostgreSQL.
- A digest mismatch returns a validation error; uploading the same part number
  again replaces the untrusted TOS part.
- A backend restart loses no durable session or part state.
- Concurrent complete requests serialize on the session row.
- Expired active sessions are claimed by a periodic backend cleanup loop using
  database row locking that is safe across replicas. The worker aborts the TOS
  multipart upload and marks the session aborted. Failures remain retryable on
  the next cleanup pass.
- New successful part activity extends expiry by 24 hours, so a slow but active
  upload is not collected.
- The deployment must confirm that its TOS credentials include multipart
  create, upload-part, complete, abort, and head permissions before rollout.

## Security boundaries

- Every operation re-authorizes the current principal and data space.
- Session IDs are high-entropy opaque identifiers but are not treated as
  authorization.
- Part numbers, lengths, digests, paths, and content types are validated at the
  HTTP boundary.
- The browser never receives raw TOS responses or request IDs that expose the
  endpoint, bucket, key, or upload ID.
- Database errors and TOS errors return stable platform error codes and do not
  leak internal storage coordinates.
- Backend request bodies remain bounded by the authoritative expected part
  size, even though there is no whole-file platform cap.

## Verification

Tests must cover:

- part-size calculation at the single/multipart threshold, 10,000-part
  boundary, 5 GiB part boundary, and TOS effective maximum;
- migration embedding and PostgreSQL repository state transitions;
- tenant, owner, space, path, expiry, length, part-number, and digest rejection;
- idempotent create, part retry, complete reconciliation, and abort;
- TOS SDK create, upload-part, complete, abort, and error mapping through fakes;
- concurrent part recording and serialized completion;
- frontend mode dispatch, incremental hashing, concurrency bound, retry policy,
  aggregate progress, refresh resume, mismatch re-upload, complete, and abort;
- Nginx streaming and 5 GiB per-part request allowance;
- unchanged behavior for existing small-file uploads and folder retries.

An isolated end-to-end harness must run with a deliberately small multipart
threshold and small parts. It must demonstrate an interrupted upload, process
restart, resume with already completed parts skipped, final byte-for-byte
equality, and no visible partial object before completion. Production
acceptance uses a non-sensitive large fixture in the user's personal space and
cleans it up after verification.

## Rollout and compatibility

The schema migration lands before the new routes are exercised because backend
migrations run at startup. The frontend treats a missing `mode` field as the
legacy single-upload response during a rolling deployment. The backend accepts
the existing create-upload request shape and keeps the existing single PUT
endpoint for old frontend pods.

Deployment rebuilds only backend and frontend. It does not rebuild
`spk-rayjob`, change training runtime images, restart RayJobs, or expose TOS to
the public network.
