import assert from 'node:assert/strict'
import test from 'node:test'

import { jobListPath, normalizeJobListPage, visibleJobScopes } from './jobListPagination.js'

test('builds a bounded server-side job page query', () => {
  assert.equal(
    jobListPath({ scope: 'mine', limit: 25, offset: 25, status: 'RUNNING', keyword: 'bev fusion' }),
    '/api/v1/jobs?scope=mine&limit=25&offset=25&status=RUNNING&keyword=bev+fusion',
  )
})

test('defaults job list requests to the authenticated submitter', () => {
  assert.equal(jobListPath(), '/api/v1/jobs?scope=mine&limit=50&offset=0')
})

test('normalizes missing pagination fields without inventing totals', () => {
  assert.deepEqual(normalizeJobListPage({ items: [{ id: 'job-1' }] }), { items: [{ id: 'job-1' }], total: 0, limit: 50, offset: 0 })
})

test('only administrators receive the team job-list scope', () => {
  assert.deepEqual(visibleJobScopes(['Engineer']), ['mine'])
  assert.deepEqual(visibleJobScopes(['TenantAdmin']), ['mine', 'team'])
  assert.deepEqual(visibleJobScopes(['SuperAdmin']), ['mine', 'team'])
})
