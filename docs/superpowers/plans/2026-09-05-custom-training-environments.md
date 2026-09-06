# Custom Training Environments Implementation Plan

## Production release — 2026-09-06

User approved publication and shared, tag-based registration. Released backend,
frontend and CLI as `release-20260906-01`, source `cc42cb2`, Helm revision 173.
Generic base built from `7de0cb5` and published as:

`harbor.wellspiking.ai/guofeng.su/raytrain-base:ray2.58.0-py310-torch2.4.1-cu121-20260906`

Shared non-default catalogue ID: `job-f0242b6e029a7068f43765d3`. Environment
description and administrator validation notes populated; prior defaults unchanged.

| Component | Digest |
| --- | --- |
| base (catalogue uses tag) | `sha256:3ec73cf863847a42bcac035309259ad9d08fd74561384cc8476020c62264d6e2` |
| backend | `sha256:fc4186931a53a4deb95e71089d646ec7647913da0e8afa21dcc5f04f35c5a67a` |
| frontend | `sha256:59c47a683e9ed3fb495e8143589c806f2250602d2113007f14a962bf40b468b3` |
| CLI release | `sha256:7178afd9fa260daac24b1d5c55c4922bfd8cdc63c61cf5c15291c8a1b4fc8f6c` |

Actual base CPU container selfcheck passed without network/GPU/host mounts:
Python3.10.14, Ray2.58.0, Torch2.4.1, CUDA runtime12.1, torchvision0.19.1,
PyArrow25.0.1, MLflow skinny3.14.0, protocol1 and pip check. GPU, NCCL and
distributed/model acceptance remain unverified; no such jobs were submitted.

Helm dry-run changed only three image lines. All three deployments have two Ready
replicas with expected image IDs. Health/help/external-submit HTTP200; served JS
contains published base tag. Downloaded Linux CLI passed SHA256 verification,
reported release-20260906-01, and authenticated `images --output json` returned
the new shared environment. Verification session was logged out afterwards.

Active `tenant-local/job-29dc380420222684984b87cf` remained RUNNING: same UID,
spec, head/worker Pod UIDs, specs and restart counters. Values backup, override,
base selfcheck report, catalogue record and build logs retained on build host at
`/root/base-release-20260906/`. Earlier development-only status below is historical.

> **For agentic workers:** Use superpowers:subagent-driven-development or superpowers:executing-plans to implement the independent units below with failing tests first.

**Goal:** Let users understand registered environments and derive dependency-only images without embedding training code.

**Architecture:** Extend the existing tenant-scoped image catalogue with optional administrator-declared environment metadata. Ship an explicit, non-default generic base build target and offline self-check plus dependency-only template. Link the submit UI to a searchable, exportable help walkthrough. Preserve tag and external-registry support.

**Tech Stack:** Go/Gin/Gorm/PostgreSQL, Vue/Node tests, Docker/Python.

## Approved scope and safety boundaries

The user approved the discussed design and explicitly requires tags and other
registries. Do not force digest entry or claim all images use Ray 2.58. KubeRay
operator and Ray package versions are separate. Do not automatically execute
user images, probe arbitrary registry URLs from the API, or expose credentials.
No production rollout, image publication, GPU allocation or default replacement
is performed in this development pass. A new base reference is not advertised as
available until build/publication succeeds. Checks supplied by administrators
are declarations, not trusted platform attestations.

## Tasks

- [ ] Backend: add optional `environment` strings `python`, `cuda`, `pytorch`,
  `mlflow`, `dependencies`, `useCases`, `validationNotes`. Test validation, API
  preservation, repository round trip and additive migration 0031 before code.
  Run `go test ./domain ./api ./repositories ./db` then full backend suite.
- [ ] UI: carry metadata in `buildCreateImageRequest`, collect it on image
  registration, render in catalogue and selected training image with explicit
  administrator-declared provenance. Test unknown fields and old image fallback,
  preserve registry/tag behavior and existing roles. Run `npm test` and build.
- [ ] Base: new explicit `raytrain-base` build target derived from pinned generic
  PyTorch foundation, platform runtime and constrained Ray 2.58. No model code.
  Provide CPU-only selfcheck, dependency template and deny-by-default build
  context. Test required runtime files and absence of training-code copies.
- [ ] Help: add `custom-environment` topic with missing-dependency decision steps,
  registry/tag instructions, manifest/selfcheck explanations, admin handoff,
  failure troubleshooting and Ray/PyTorch/KubeRay terminology. Test shared
  Markdown export and topic presence before implementation.
- [ ] Review integration and run all tests. Record actual verification limits;
  do not equate static Docker checks or CPU selfcheck with GPU acceptance.

## Follow-on requiring separate integration

Registry-side tag resolution and trusted isolated image verification are not
represented by the existing catalogue. Keep current reference behavior visible
and never claim automatic pinning or trusted scanning exists. Introducing these
requires registry credential routing, SSRF protections and an isolated executor;
it must not be approximated by an unchecked URL fetch or running user containers
on the build host.

## Local implementation and verification

Implemented on `codex/custom-training-environments`, without production changes:

- Migration0031 and validated optional environment metadata; legacy images show
  unknown fields rather than fabricated versions. Existing catalogue permissions
  and tag/digest acceptance preserved. Existing registration scope editing is
  unchanged; this pass adds environment input to new registrations only.
- Environment details in catalogue expansion and selected training image, plus
  a help link. All environment records remain administrator-declared.
- Help now has 22 topics; custom environment walkthrough includes individual
  Dockerfile/requirements/.dockerignore downloads and appears in Markdown export.
- Explicit-only generic base recipe, constraints, CPU report and derived example;
  no published reference is claimed and no default image is replaced.
- Frontend 309 tests and build passed; backend full tests and build passed;
  base recipe/selfcheck 5 tests passed; shell syntax and diff checks passed.

Remaining release gates: real base-image build and push, actual image dependency
report, non-disruptive CPU container acceptance, then separately authorized GPU
and distributed acceptance. Docker is unavailable locally; static recipe tests
are not a substitute for that build. The additive database migration is not yet
applied to production. No image auto-verification service or tag-to-digest
resolution was added; UI explains these limits explicitly.

## User-requested command-first addition

- Added authenticated, read-only `spk-rayjob images [--output json]` using the
  existing training catalogue client. No pull, execution or admin mutation.
  Tests cover authentication, JSON/text, cancellation and terminal control codes.
- External-submit page now has Bash/zsh and PowerShell custom registered-image
  examples; native Ray supplies all six required resource metadata keys and
  serializes values safely. Real Bash/zsh argument-capture tests exercise quotes.
- Added command-recipes help covering images, data versions, DDP/mount, managed
  streaming/sites, status/logs/resume and native submission IDs. Native advanced
  metadata matches `backend/rayapi/translator.go`; metadata values are strings,
  with sites represented as a JSON-encoded array string. No unsupported training
  tuning metadata is documented as available.
- Ray CLI syntax checked against the official reference:
  https://docs.ray.io/en/latest/cluster/running-applications/job-submission/cli.html
- Existing CLI binaries must be rebuilt/released before `images` is available.
  This development pass did not rebuild the published CLI or submit smoke jobs.
