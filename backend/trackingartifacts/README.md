# External Run artifacts

The tracking API authorizes the current caller against the external Run before
calling this package. `Scope.OwnerID` is the **Run owner's identity**, including
when a separately authorized integration accesses that Run. A caller cannot
supply a bucket, prefix, absolute path, or another user's data-space root.

Each artifact is an immutable file under a generated 128-bit ID. File names are
single path components. This release accepts non-empty files up to 20 GiB, with
fixed 8 MiB parts (the final part may be shorter). There are at most 16 pending
uploads and 100 GiB of pending plus ready declarations per tenant/owner. Expired
pending uploads continue to reserve quota until cancelled; this prevents an
expired or interrupted upload from hiding persistent storage consumption.

Initialization uses the caller's stable idempotency key. Reusing a key with a
different Run or file declaration is a conflict. Upload requests contain one
part and its SHA256. The API must enforce the request-body size and hard read
deadline. The service buffers at most one part before locking the artifact, then
uses immutable object creation. Retrying the same part repairs a missing object
but never changes its registered digest. Complete streams all parts, verifying
both part hashes and the declared whole-file hash, before publishing READY.
Nothing is unpacked, imported, executed, or deserialized.

PostgreSQL serializes each artifact's object operation with a row lock. Lock
waits are limited to 5 seconds; object operations have a 15-minute context
deadline. Completion of a large file therefore occupies one database connection
while reading the file. Owner-budget admission uses a separate short advisory
transaction lock and does not block unrelated object IO for that owner. A
process failure rolls back the database operation; immutable objects may remain
and are recovered by retrying the same part or cancelling the upload.

Cancel only accepts unpublished uploads. It deletes the artifact's finite set
of possible part keys (at most 2560 keys), including parts written immediately
before a database/process failure. No prefix listing, personal-data cleanup, or
user-supplied deletion key is used. Cleanup failure leaves the upload pending
and retains the full reservation; retrying cancel is safe. Ready artifacts have
no overwrite or delete operation in this release. Their database declarations
and part metadata are protected by immutability triggers.

Download opens one part stream at a time, checks size and SHA256, and closes the
current stream on cancellation or error. HTTP must use attachment disposition,
no-store and nosniff, and must not expose the TOS key or credentials. The current
release streams complete files and does not provide HTTP byte-range resume.
This API does not implement MLflow's native artifact upload URI protocol.
