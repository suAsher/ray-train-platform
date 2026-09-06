# Custom Training Environments Implementation Plan

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
