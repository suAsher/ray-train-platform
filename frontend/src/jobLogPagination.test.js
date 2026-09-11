import test from 'node:test'
import assert from 'node:assert/strict'

import { collectAllLogPages, logPagePath, mergeLogEntries, normalizeLogPage } from './jobLogPagination.js'

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

test('collects every backward page and reports export progress', async () => {
  const pages = [
    {
      items: [{ timestamp: '2026-08-22T16:00:03Z', line: 'three', stream: { pod: 'worker-1' } }],
      page: { hasMore: true, nextCursor: 'cursor-3' },
    },
    {
      items: [{ timestamp: '2026-08-22T16:00:02Z', line: 'two', stream: { pod: 'worker-1' } }],
      page: { hasMore: true, nextCursor: 'cursor-2' },
    },
    {
      items: [{ timestamp: '2026-08-22T16:00:01Z', line: 'one', stream: { pod: 'worker-1' } }],
      page: { hasMore: false, nextCursor: 'cursor-1' },
    },
  ]
  const paths = []
  const progress = []

  const logs = await collectAllLogPages(
    async path => {
      paths.push(path)
      return pages.shift()
    },
    'job/a',
    { limit: 2, onProgress: count => progress.push(count) },
  )

  assert.deepEqual(logs.map(entry => entry.text), ['one', 'two', 'three'])
  assert.deepEqual(progress, [1, 2, 3])
  assert.equal(paths[0], '/api/v1/jobs/job%2Fa/logs?limit=2&direction=backward')
  assert.match(paths[1], /before=cursor-3/)
  assert.match(paths[2], /before=cursor-2/)
})

test('preserves genuinely duplicated log lines during a complete export', async () => {
  const duplicate = { timestamp: '2026-08-22T16:00:01Z', line: 'same', stream: { pod: 'worker-1' } }

  const logs = await collectAllLogPages(
    async () => ({ items: [duplicate, duplicate], page: { hasMore: false } }),
    'job-1',
  )

  assert.equal(logs.length, 2)
  assert.deepEqual(logs.map(entry => entry.text), ['same', 'same'])
})

test('rejects a non-advancing export cursor instead of looping forever', async () => {
  const page = {
    items: [{ timestamp: '2026-08-22T16:00:03Z', line: 'same', stream: { pod: 'worker-1' } }],
    page: { hasMore: true, nextCursor: 'stalled' },
  }

  await assert.rejects(() => collectAllLogPages(async () => page, 'job-1'), /cursor did not advance/)
})
