# Editable Help Documents Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Let only SuperAdmin create, edit, publish, unpublish and restore platform help documents while all authenticated readers see published snapshots only.

**Architecture:** Gorm stores documents and immutable revisions. A compare-and-swap version protects concurrent edits; publishing selects a snapshot independently of the current draft. The existing help topics become an embedded, insert-only seed with stable IDs. The Vue reader and editor share safe Markdown rendering, while the server enforces permissions independently of UI visibility.

**Tech Stack:** Go/Gin/Gorm, PostgreSQL migration 0032, Vue/Element Plus, Node test runner.

## Contract

Document fields: `id`, `title`, `category`, `sortOrder`, `markdown`, `version`, `publishedVersion`, `updatedAt`, `updatedBy`.
IDs are immutable lowercase alphanumeric/hyphen slugs (1–80 characters). Title <=200 characters, category <=100, markdown nonempty <=512 KiB, sort order integer within ±100000. Maximum JSON request 600 KiB.

Authenticated `GET /api/v1/help/documents` returns `{items: [published snapshots]}`.
Every `/api/v1/admin/help/documents` route requires an interactive SuperAdmin session:

| Method/path | Body | Result |
| --- | --- | --- |
| GET collection | — | `{items: [current drafts]}` |
| POST collection | id/title/category/sortOrder/markdown | created draft |
| PUT /:id | title/category/sortOrder/markdown/expectedVersion | new draft revision |
| POST /:id/publish | expectedVersion | publish current draft, advance version |
| POST /:id/unpublish | expectedVersion | hide from readers, preserve history |
| GET /:id/history | — | `{items: [snapshots with action]}` |
| POST /:id/restore | expectedVersion/restoreVersion | copy old revision to a new draft |

Return 409 on concurrent/stale updates; never overwrite another administrator's work. Restore does not publish. No permanent delete.

## Task 1: Seed current help faithfully

Files: `frontend/src/help/content.js`, `frontend/src/help/seed.test.js`, `scripts/help-seed.mjs`, `backend/helpdocs/seed.json`.

- [x] Add failing tests that all topic IDs and category/order survive conversion, templates retain downloadable fence metadata, and generated JSON matches source.
- [x] Run `cd frontend && node --test src/help/seed.test.js`; observe missing serializer/seed failures.
- [x] Extract `renderHelpSectionMarkdown(section, {origin})` from the existing serializer; make seed script output deterministic JSON to stdout or an apply_patch patch (never overwrite live DB).
- [x] Generate checked-in seed with all existing topics. Preserve command snippets exactly; use fence info `lang filename=example.py` for downloadable templates.
- [x] Rerun seed and existing help tests; assert IDs are unique and valid.

## Task 2: Persistence and authorization

Files: new `backend/domain/help_documents.go`, `backend/repositories/help_documents.go`, `backend/api/help_documents.go` and tests; migration0032, migration version test, Handler/main wiring, seed embedding.

- [x] Write lifecycle tests before implementation: draft invisible; publish visible; edit preserves published content; unpublish hides; restore creates draft; stale update returns conflict; seed is insert-only.
- [x] Write route tests denying Member/TenantAdmin, unauthenticated and PAT administration, including history and restore.
- [x] Implement transactions with optimistic version checks and immutable revision snapshots, explicit bounds, generic server errors and request body limits.
- [x] Seed once per missing ID at startup, with conflict-do-nothing; no frontend fallback that resurrects unpublished docs.
- [x] Run `cd backend && go test ./repositories ./api ./db ./...` and `go build ./...`.

## Task 3: Reader and SuperAdmin editor

Files: Help view, API wrapper, Markdown reader component, document editor and tests.

- [x] Write failing tests for API routes/body, published-only search/export, dangerous URL/raw HTML escaping, filename download safety and draft transitions.
- [x] Load published docs for ordinary reading, preserve `/help#id`, show loading/error/retry/empty states, do not expose draft snippets in search/download.
- [x] Add SuperAdmin-only management entry, immutable ID on edit, title/category/order fields and Markdown textarea with shared preview.
- [x] Add explicit save/publish/unpublish actions, revision author/time/preview/restore, confirmation and unsaved-change protection; stale conflict preserves local text.
- [x] Preserve code copy and template download via fenced metadata. Do not interpret raw HTML or unsafe URL schemes.
- [x] Run `cd frontend && npm test && npm run build`.

## Task 4: Acceptance and handoff

- [x] Review spec coverage, then independent security/code review; resolve findings.
- [x] Test browser workflow locally with mocked authenticated APIs if no local DB environment is available; report this limitation rather than claiming production validation.
- [x] Add administrator usage documentation including draft/published separation, concurrency, recovery and migration behavior.
- [x] Run fresh full tests/builds, `git diff --check`, inspect final diff and report exact verification results.
- [x] Leave changes local on `codex/editable-help-docs`; do not deploy, push or touch any training resources without a new request.

## Verification record

2026-09-06: Backend `go test ./...` and `go build ./...` passed. Frontend 324/324 tests and production build passed. New backend help API/store/domain paths have approximately 90% statement coverage; this is not a claim of whole-repository coverage. Local Playwright browser with mocked authenticated APIs verified create/save/publish/edit/history/restore/unpublish, ordinary-user visibility, error/retry and empty/deep-link states. PostgreSQL migration and production integration are not yet exercised; no deployment or training resource changes. Existing layout icon warnings and existing production bundle-size warnings remain outside this feature. Independent backend and frontend reviews completed; offline-link and stale-response findings fixed and retested.
