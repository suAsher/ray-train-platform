# Training failure diagnosis and CLI completion

User approved implementation on 2026-09-29. Delivery is reviewed source and builder validation; production deployment and publishing are separate actions.

## Shared diagnosis

Expose one read-only, jobs:read-scoped `/api/v1/jobs/:id/diagnosis` endpoint using the same job visibility as logs. Portal and `spk-rayjob diagnose` consume this response. Preserve authoritative task state and statusMessage unchanged. Find the earliest recognizable exception in retained lifecycle logs, include its timestamp, pod/container, bounded surrounding context, and subsequent communication errors. Call this observed evidence, not a proven root cause. Report filtered/partial/unavailable coverage explicitly. Bound queries, response size, and duration; do not scan all logs into the API process or change the database schema.

Completion text/checkpoint paths are unverified clues. A later launcher crash can be presented as a possible teardown failure; it must never promote a FAILED task to SUCCEEDED. Unknown or missing evidence stays unknown. Existing raw log export/follow semantics remain intact.

## Runtime lifecycle

Launch only platform-owned subprocesses in their own process groups. Poll distributed launcher exits without waiting for every worker to finish. On a confirmed nonzero exit or launch failure, retain the original error code and terminate sibling groups with finite TERM/KILL grace. Clean remaining children after root exits and on cancellation signals. Do not terminate based on regex matches, GPU utilization, or generic progress timeouts. Report start/exit/cleanup as structured runtime log events. Single-node shell wrappers that never exit despite inner failure remain a documented limitation.

## Shell completion

`spk-rayjob completion bash|zsh` emits scripts; no automatic shell profile changes. Complete commands, subcommands, supported flags, enums and file paths. Task-ID suggestions use the existing authenticated visible-jobs API and include name/state where shell support permits. Network failure is silent, requests have a short deadline, and caching must never leak across server/principal changes. Never offer tokens, execute candidate values, or issue mutations while completing.

## Verification

Tests cover first exception before NCCL cascades, completion text before fatal launcher exit, empty/truncated/unavailable logs, cross-tenant authorization, safe shell quoting, stale/failed completion requests, process-tree cleanup and preservation of the first failed exit. All tests/lint/compilation run on the existing builder in isolated candidates. Real PostgreSQL lifecycle tests must run if database/reconciliation lifecycle logic changes. Runtime behavior tests use dedicated synthetic processes, never existing training tasks. Real GPU and production acceptance remain separate evidence.
