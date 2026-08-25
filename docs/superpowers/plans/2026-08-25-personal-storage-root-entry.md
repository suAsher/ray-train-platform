# Personal Storage Root Entry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/mnt/storage/me` the single directly browsable personal-file entry on the “我的数据” page.

**Architecture:** Add a governed `my-storage` logical root backed by the existing personal binding. Preserve `my-files` and `my-runs` for compatibility, while the page and new training-input picker prefer the direct root.

**Tech Stack:** Vue 3, Element Plus, Node test runner, Vite.

---

### Task 1: Personal storage overview

**Files:**
- Modify: `backend/domain/data_space.go`
- Modify: `backend/api/data_spaces.go`
- Modify: `backend/api/submission_service.go`
- Modify: `frontend/src/dataSpaceActions.js`
- Modify: `frontend/src/components/DataSpacePicker.vue`
- Modify: `frontend/src/views/DataCache/index.vue`
- Test: `backend/domain/data_space_test.go`
- Test: `backend/api/data_spaces_test.go`
- Test: `backend/domain/resolved_data_mount_test.go`
- Test: `frontend/src/dataSpaceActions.test.js`
- Test: `frontend/src/dataSpaceEmptyState.test.js`

- [x] **Step 1: Write the failing test**

Assert that the backend exposes `my-storage` at the actual personal root and that the page renders one direct root without nested navigation cards.

- [x] **Step 2: Run test to verify it fails**

Run: `cd frontend && node --test src/dataSpaceEmptyState.test.js`

Expected: FAIL because `my-storage` and the single-root catalogue filters are absent.

- [x] **Step 3: Write minimal implementation**

Add `my-storage`, map it to the existing personal binding, and filter legacy aliases from the new page and input picker while retaining rolling compatibility.

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

Confirm existing output and Checkpoint paths remain under `runs/`, while only new personal-input selection and Portal browsing use the direct root.
