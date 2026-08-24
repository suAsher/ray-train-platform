import test from 'node:test'
import assert from 'node:assert/strict'

import { createSingleFlight, nextLogRequest } from './jobLogPolling.js'

test('starts with a backward tail and follows new logs from an exact cursor', () => {
  assert.deepEqual(nextLogRequest([]), {
    limit: 2000,
    direction: 'backward',
    cursor: '',
  })

  const entries = [
    { timestamp: '2026-08-24T08:00:00Z', text: 'one' },
    { timestamp: '2026-08-24T08:00:01Z', text: 'two' },
    { timestamp: '2026-08-24T08:00:01Z', text: 'three' },
  ]
  assert.deepEqual(nextLogRequest(entries), {
    limit: 2000,
    direction: 'forward',
    cursor: '2026-08-24T08:00:01.000000001Z',
  })

  assert.deepEqual(nextLogRequest(entries, '2026-08-24T08:00:01Z~17'), {
    limit: 2000,
    direction: 'forward',
    cursor: '2026-08-24T08:00:01Z~17',
  })
})

test('coalesces overlapping refreshes and allows the next refresh after completion', async () => {
  let resolveOperation
  let calls = 0
  const refresh = createSingleFlight(async () => {
    calls += 1
    await new Promise((resolve) => { resolveOperation = resolve })
    return calls
  })

  const first = refresh()
  const overlapping = refresh()
  assert.equal(calls, 1)
  assert.equal(first, overlapping)

  resolveOperation()
  assert.equal(await first, 1)

  const next = refresh()
  assert.equal(calls, 2)
  resolveOperation()
  assert.equal(await next, 2)
})
