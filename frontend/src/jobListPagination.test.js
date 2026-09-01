import assert from 'node:assert/strict'
import test from 'node:test'

import { jobListPath, normalizeJobListPage } from './jobListPagination.js'

test('builds a bounded server-side job page query', () => {
  assert.equal(
    jobListPath({ scope: 'mine', limit: 25, offset: 25, status: 'RUNNING', keyword: 'bev fusion' }),
    '/api/v1/jobs?scope=mine&limit=25&offset=25&status=RUNNING&keyword=bev+fusion',
  )
})

test('normalizes missing pagination fields without inventing totals', () => {
  assert.deepEqual(normalizeJobListPage({ items: [{ id: 'job-1' }] }), { items: [{ id: 'job-1' }], total: 0, limit: 50, offset: 0 })
})
