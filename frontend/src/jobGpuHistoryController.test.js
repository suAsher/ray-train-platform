import assert from 'node:assert/strict'
import test from 'node:test'

import { jobMetricSummary, normalizeGPUHistory } from './gpuMetrics.js'
import { createJobGPUHistoryController } from './jobGpuHistoryController.js'

function deferred() {
  let resolve
  let reject
  const promise = new Promise((onResolve, onReject) => {
    resolve = onResolve
    reject = onReject
  })
  return { promise, resolve, reject }
}

function harness(fetchHistory) {
  const histories = []
  const warnings = []
  const loading = []
  const resets = []
  const scheduled = []
  const cleared = []
  const controller = createJobGPUHistoryController({
    fetchHistory,
    normalizeHistory: (payload) => ({ normalized: payload }),
    onHistory: (history) => histories.push(history),
    onWarning: (warning) => warnings.push(warning),
    onLoading: (value) => loading.push(value),
    onReset: (history) => resets.push(history),
    setInterval: (callback, milliseconds) => {
      scheduled.push({ callback, milliseconds })
      return `timer-${scheduled.length}`
    },
    clearInterval: (timer) => cleared.push(timer),
  })
  return { controller, histories, warnings, loading, resets, scheduled, cleared }
}

test('first successful load normalizes and publishes job GPU history', async () => {
  const calls = []
  const state = harness(async (jobId, window) => {
    calls.push([jobId, window])
    return { devices: [{ uuid: 'GPU-0' }] }
  })

  await state.controller.start('job-a', '1h')

  assert.deepEqual(calls, [['job-a', '1h']])
  assert.deepEqual(state.histories, [{ normalized: { devices: [{ uuid: 'GPU-0' }] } }])
  assert.deepEqual(state.warnings, [''])
  assert.deepEqual(state.loading, [true, false])
})

test('failed refresh keeps the previous success and publishes a retry warning', async () => {
  let attempt = 0
  const state = harness(async () => {
    attempt += 1
    if (attempt === 1) return { window: '1h', devices: [{ uuid: 'GPU-0' }] }
    throw new Error('Prometheus unavailable')
  })
  await state.controller.start('job-a', '1h')

  await state.controller.refresh()

  assert.equal(state.histories.length, 1)
  assert.deepEqual(state.histories[0], { normalized: { window: '1h', devices: [{ uuid: 'GPU-0' }] } })
  assert.equal(state.warnings.at(-1), 'Prometheus unavailable')
})

test('30-second interval callback retries GPU history', async () => {
  let attempt = 0
  const state = harness(async () => {
    attempt += 1
    if (attempt === 1) throw new Error('temporary failure')
    return { attempt }
  })

  await state.controller.start('job-a', '1h')
  assert.equal(state.scheduled.length, 1)
  assert.equal(state.scheduled[0].milliseconds, 30000)

  await state.scheduled[0].callback()

  assert.equal(attempt, 2)
  assert.deepEqual(state.histories, [{ normalized: { attempt: 2 } }])
  assert.equal(state.warnings.at(-1), '')
})

test('window change fetches the selected window immediately', async () => {
  const calls = []
  const state = harness(async (jobId, window) => {
    calls.push([jobId, window])
    return { jobId, window }
  })
  await state.controller.start('job-a', '1h')

  await state.controller.changeWindow('6h')

  assert.deepEqual(calls, [['job-a', '1h'], ['job-a', '6h']])
  assert.deepEqual(state.histories.at(-1), { normalized: { jobId: 'job-a', window: '6h' } })
})

test('job change resets state and ignores an old unresolved response', async () => {
  const oldRequest = deferred()
  const state = harness((jobId, window) => (
    jobId === 'job-old' ? oldRequest.promise : Promise.resolve({ jobId, window })
  ))
  const firstLoad = state.controller.start('job-old', '1h')

  await state.controller.changeJob('job-new')
  oldRequest.resolve({ jobId: 'job-old', window: '1h' })
  await firstLoad

  assert.deepEqual(state.resets, [{ normalized: null }])
  assert.deepEqual(state.histories, [{ normalized: { jobId: 'job-new', window: '1h' } }])
})

test('stop clears the timer and invalidates an unresolved response', async () => {
  const request = deferred()
  const state = harness(() => request.promise)
  const firstLoad = state.controller.start('job-a', '1h')

  state.controller.stop()
  request.resolve({ devices: [{ uuid: 'GPU-late' }] })
  await firstLoad

  assert.deepEqual(state.cleared, ['timer-1'])
  assert.deepEqual(state.histories, [])
  assert.deepEqual(state.warnings, [])
})

test('controller integration keeps empty and partial metrics from becoming false zero', async () => {
  const payloads = [
    {
      devices: [
        { uuid: 'GPU-0', series: { utilizationPercent: [{ timestamp: '2026-08-24T12:59:30Z', value: 75 }] } },
        { uuid: 'GPU-1' },
      ],
    },
    { devices: [{ uuid: 'GPU-0' }, { uuid: 'GPU-1' }] },
  ]
  const summaries = []
  const controller = createJobGPUHistoryController({
    fetchHistory: async () => payloads.shift(),
    normalizeHistory: normalizeGPUHistory,
    onHistory: (history) => summaries.push(jobMetricSummary(history)),
    setInterval: () => 'timer',
    clearInterval: () => {},
  })

  await controller.start('job-a', '1h')
  await controller.refresh()

  assert.equal(summaries[0].averageUtilizationPercent, 75)
  assert.equal(summaries[0].totalPowerWatts, null)
  assert.deepEqual(summaries[0].coverage.utilizationPercent, { sampled: 1, total: 2 })
  assert.equal(summaries[1].averageUtilizationPercent, null)
  assert.equal(summaries[1].maximumTemperatureCelsius, null)
})
