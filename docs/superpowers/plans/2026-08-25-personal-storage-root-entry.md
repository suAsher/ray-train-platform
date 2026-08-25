# Personal Storage Root Entry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a safe `/mnt/storage/me` overview and navigation entry to the “我的数据” page.

**Architecture:** Keep the existing backend data-space catalogue unchanged. Render a frontend-only overview that delegates browsing and mutation to the existing `my-files` and `my-runs` spaces.

**Tech Stack:** Vue 3, Element Plus, Node test runner, Vite.

---

### Task 1: Personal storage overview

**Files:**
- Modify: `frontend/src/dataSpaceEmptyState.test.js`
- Modify: `frontend/src/views/DataCache/index.vue`

- [x] **Step 1: Write the failing test**

Assert that the page contains the `/mnt/storage/me` overview, both child paths, and navigation actions for `my-files` and `my-runs`.

- [x] **Step 2: Run test to verify it fails**

Run: `cd frontend && node --test src/dataSpaceEmptyState.test.js`

Expected: FAIL because the overview and navigation markers are absent.

- [x] **Step 3: Write minimal implementation**

Add one overview section and a `selectSpaceByID(id)` helper that delegates to the existing `selectSpace` function.

- [x] **Step 4: Run focused and full verification**

Run:

```bash
cd frontend
node --test src/dataSpaceEmptyState.test.js
npm test
npm run build
```

Expected: all tests and the production build succeed.

- [x] **Step 5: Review the diff**

Confirm no backend, storage, Kubernetes, or Ray workload behavior changed.
