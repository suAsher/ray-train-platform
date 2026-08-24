import test from 'node:test'
import assert from 'node:assert/strict'

import { logPagePath, mergeLogEntries, normalizeLogPage } from './jobLogPagination.js'

test('normalizes the API page and preserves stream identity', () => {
  const normalized = normalizeLogPage({
    items: [{ timestamp: '2026-08-22T16:00:02Z', line: 'done', stream: { pod: 'worker-1', container: 'ray-worker' } }],
    page: { direction: 'backward', limit: 2000, hasMore: true, nextCursor: '2026-08-22T16:00:02Z' },
  })

  assert.deepEqual(normalized.logs, [{
    node: 'worker-1', text: 'done', timestamp: '2026-08-22T16:00:02Z',
    stream: { pod: 'worker-1', container: 'ray-worker' },
  }])
  assert.equal(normalized.hasMore, true)
  assert.equal(normalized.nextCursor, '2026-08-22T16:00:02Z')
})

test('merges refresh and older pages chronologically without duplicates or mutation', () => {
  const current = [
    { timestamp: '2026-08-22T16:00:02Z', text: 'two', node: 'worker-1', stream: { pod: 'worker-1' } },
    { timestamp: '2026-08-22T16:00:03Z', text: 'three', node: 'worker-1', stream: { pod: 'worker-1' } },
  ]
  const incoming = [
    { timestamp: '2026-08-22T16:00:01Z', text: 'one', node: 'worker-1', stream: { pod: 'worker-1' } },
    { timestamp: '2026-08-22T16:00:02Z', text: 'two', node: 'worker-1', stream: { pod: 'worker-1' } },
  ]

  const merged = mergeLogEntries(current, incoming)

  assert.deepEqual(merged.map((entry) => entry.text), ['one', 'two', 'three'])
  assert.equal(current.length, 2)
  assert.equal(incoming.length, 2)
})

test('builds backward and forward cursor queries safely', () => {
  assert.equal(
    logPagePath('job/a', { limit: 2000, direction: 'backward', cursor: '2026-08-22T16:00:02+08:00' }),
    '/api/v1/jobs/job%2Fa/logs?limit=2000&direction=backward&before=2026-08-22T16%3A00%3A02%2B08%3A00',
  )
  assert.match(logPagePath('job-1', { direction: 'forward', cursor: 'cursor' }), /after=cursor/)
})
