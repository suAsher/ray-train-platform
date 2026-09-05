# UI help completion plan

Approved by the user after the UI help audit. Scope is local documentation,
copyable commands, discoverability and regression tests; no production rollout.

## Design

Keep the existing help navigation and stable topic IDs. Enrich each topic with
prerequisites, success checks, troubleshooting and related in-app links. Add an
upload walkthrough. Keep page and Markdown export driven by the same objects.
Separate OS-specific external-submit generators from Vue rendering so copied
commands can be tested without browser or production credentials.

## Work and ownership

- [ ] Topic worker: `frontend/src/help/topics.js`, `snippets.js`, focused topic
  modules if needed, and `frontend/src/helpWalkthrough.test.js`. Write tests for
  explicit resource flags, site selection, streaming adapters, upload resume and
  topic metadata before updating content. Preserve existing topic IDs.
- [ ] External-submit worker: `frontend/src/views/ExternalSubmit/index.vue`,
  `frontend/src/help/externalSubmit.js` and its tests. Test checksum rejection
  before installation and shell-specific login syntax before implementing.
- [ ] Main: `frontend/src/help/content.js`, `views/Help/index.vue`, and
  `helpContent.test.js`. Test metadata export, bounded search, Markdown table
  escaping and route hash synchronization before implementing. Render metadata
  and related links on the page; add keyword search and a reviewed date.
- [ ] Review the integrated wording against API/CLI and user permissions.
- [ ] Run `npm test` and `npm run build` from `frontend`; run generated shell
  tests without network or writes outside temporary fixtures. Browser smoke
  check if available. PowerShell execution is not claimed without PowerShell.

## Acceptance

Every topic states prerequisites, expected outcome and failure actions. DDP and
Ray Data sampler rules do not contradict. Streaming is described as reference
resolution through a compatible adapter, with multi-GPU acceptance pending.
Installation never continues after a failed checksum. Downloaded documentation
contains the same steps and metadata as the UI. Existing tasks and deployment
remain untouched.

## Verification result — 2026-09-05

All work items above are implemented and locally reviewed. Integrated content
covers 20 topics, with prerequisites, success checks, recovery and related links.
Generated commands are shared by the external-submit page and command tests;
existing page-source regression tests now follow that extracted module.

- Frontend: 299 tests passed; production build passed (existing chunk-size and
  third-party annotation warnings remain).
- Bash/zsh command syntax and checksum rejection exercised using temporary
  fixtures; PowerShell structurally checked only, not executed.
- Browser: actual Help component rendered; keyword search narrowed to one topic,
  upload navigation selected `#uploads`, and Back restored the previous topic.
  Preview was local-only and its temporary HTML entry was removed afterwards.
- No backend changes, production deployment, or training-resource operations.

## Authorized deployment — 2026-09-05

After local acceptance the user requested deployment. Published frontend only:

- Source: `96e6f02f9f1e9e1ed24b118ecab3edbce5ef881e`.
- Image: `release-20260905-05`, digest
  `sha256:a323aecdcc8eb00eacd18f5165c8145b519f4b3a8c8bdc6fe72e3afda8c875df`.
- Helm revision 172; server dry-run differed only in the frontend image line.
- Both frontend replicas Ready with the expected image ID. Health, help and
  external-submit routes returned HTTP 200; the served help JavaScript contained
  the new upload walkthrough and success-check guidance.
- Active `tenant-local/job-29dc380420222684984b87cf` remained RUNNING with the
  same UID and spec; head/worker Pod UIDs, specs and restart counts (zero) matched
  the pre-release snapshot. No backend or training images were updated.
- Pre-release repeat: 299 tests passed, frontend build passed, production npm
  audit reported zero vulnerabilities. PowerShell execution remains unverified.
- Values backup, minimal override and build log are retained on the build host
  under `/root/ui-help-release-20260905/` for rollback and traceability.
