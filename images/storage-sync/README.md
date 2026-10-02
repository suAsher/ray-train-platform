# Storage sync worker

This image implements the administrator sync protocol. It has no Kubernetes API client and spawns no subprocesses. A single process serializes file transfers and parts; configured concurrency values are upper bounds, never an invitation to bypass the controller's exclusive writer claim.

`python3 -m storage_sync.worker --request /config/request.json --work-dir /work/<run> --callback-token-file /var/run/storage-sync/token` handles `PREVIEW` and `BROWSE` with a scoped read gateway. `TRANSFER` additionally takes `--tos-config /var/run/raytrain/tosutil/config`. The existing tosutil `ak`, `sk`, `endpoint`, `region`, and optional `token` keys are consumed in memory; the long aliases `accessKeyID`, `secretAccessKey`, and `securityToken` remain supported. Endpoint and region can be provided by the controller's `STORAGE_SYNC_TOS_ENDPOINT` and `STORAGE_SYNC_TOS_REGION` environment variables. Secrets and signed URLs are never logged.

Every execution first claims its attempt using the downward-API Pod UID. A duplicate Pod exits before accessing credentials or changing checkpoint files. `RECOVER` is a separate read-only mode that skips claiming and replays the original durable terminal receipt unchanged; it never invents a completed state or a request-drain assertion.

## Correctness boundaries

- IDC descriptors use `openat`, `O_NOFOLLOW`, regular-file checks, and metadata verification before and after reads. Symlinks and special files fail the scan. Both verification modes currently hash IDC files conservatively; metadata mode may later optimize unchanged files without weakening the displayed limitation.
- TOS ETags are opaque identity guards. Verification uses the service CRC64 or an actual SHA256 read, never an ETag-as-MD5 assumption or untrusted user metadata.
- `METADATA` reuses a previous successful checksum only when source and destination identities still match. `CONTENT` reads source content. Existing targets without comparable checksums are read to establish a verified baseline.
- The controller resolves the final destination prefix, including the directory layout. The worker must not append the selected source basename again.
- No path deletes a completed destination object. Target-only files remain unchanged. Cancelling aborts only upload IDs recorded in this run's checkpoint; pausing keeps them.
- Checkpoints and manifests are fsynced before atomic rename. Multipart checkpoints bind source, target, run, configuration, and part size; resume re-lists every remote part before reuse.
- Callback counters are cumulative and sequence-numbered. Source and transfer totals stay fixed across mappings. Receipts are durable before callback. A successful Pod exit alone is not a completion receipt.
- A safely drained, durably recorded and acknowledged `PAUSED` or `CANCELLED` receipt is a normal Worker exit (`0`), so Kubernetes shows `Completed`; the platform keeps the actual paused/cancelled state. Failure, uncertain writes, receipt-persistence failure and missing final callback acknowledgement still exit nonzero. Receiving a stop command alone never proves completion.
- The per-file scan callback updates discovered counts before the full manifest exists. Mapping progress and completed file results are reported independently of overall totals.
- Static-credential writer errors with unknown server outcome permanently set `requestsDrained=false` for that attempt. The controller must retain the lock until its isolation/drain requirements are satisfied. No SDK automatic write retry is enabled.

## SDK capability gate

The runtime pins `tos==2.9.3`. `put_object` and `copy_object` have target `if_match` and `forbid_overwrite`. Every source copy and part copy carries source ETag/version conditions. `complete_multipart_upload` only supports `forbid_overwrite`, so manually managed multipart uploads are allowed for new destinations and atomically reject a newly appeared target.

Updating an existing object larger than 5 GiB is rejected during read-only preflight (`UNSUPPORTED_CONDITIONAL_MULTIPART_UPDATE`). The SDK's high-level `upload_file` and `resumable_copy_object` are deliberately unused because their completion path omits the required destination guards. Existing objects up to 5 GiB use guarded single PUT/copy, with no multipart resume for that individual request. Official limits: [CopyObject](https://docs.volcengine.com/docs/TorchObjectStorage/copyobject?lang=en) and [object upload overview](https://docs.volcengine.com/docs/TorchObjectStorage/OverviewofObjectUploadNodejsSDK?lang=zh).

Plan bandwidth is zero (inherit the platform ceiling) or at least 100 KiB/s. Only when both plan and platform values are zero is there no configured limit. The SDK's service-side copy range is 100 KiB/s through 100 MiB/s; larger requested upper bounds use the stricter 100 MiB/s service cap. IDC upload/read uses the SDK token-bucket limiter. Server-side copy progress records logical bytes separately from IDC network bytes.

## Builder verification

Run only on the approved build host, from its isolated candidate worktree:

```sh
PYTHONPATH=images/storage-sync python3 -m unittest discover -s images/storage-sync/tests -v
```

Unit tests use standard-library fakes and require no credentials. Real TOS acceptance must use the authorized administrator's dedicated test subtree and must separately prove service-side conditional writes and multipart completion races; mocked assertions are not that proof.

`acceptance.py --config <existing-secret-file> --bucket <approved-bucket> --prefix <own-test-root>` creates a fresh UUID child under the approved test root. It exercises 1005 tiny objects, one 6 MiB multipart object, preserved target JSON, source/target copy races, conditional multipart completion, pause/resume, and cancellation. Cleanup only visits keys and multipart IDs created or explicitly planned by that invocation. Run it on the builder after verifying the authenticated user's stable personal storage root; never pass an existing dataset prefix as the test root.
