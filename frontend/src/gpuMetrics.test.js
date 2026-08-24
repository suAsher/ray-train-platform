import test from 'node:test'
import assert from 'node:assert/strict'

import {
  GPU_TIME_WINDOWS,
  metricChartSeries,
  nodeMetricSummary,
  normalizeGPUHistory,
  sampleFreshness,
} from './gpuMetrics.js'

const historyPayload = {
  window: '1h',
  stepSeconds: 30,
  startedAt: '2026-08-24T12:00:00Z',
  endedAt: '2026-08-24T13:00:00Z',
  devices: [
    {
      uuid: 'GPU-1', nodeName: 'node-a', index: '0', podName: 'job-1-worker',
      series: {
        utilizationPercent: [
          { timestamp: '2026-08-24T12:59:00Z', value: 80 },
          { timestamp: '2026-08-24T12:59:30Z', value: 100 },
        ],
        memoryUsedMib: [{ timestamp: '2026-08-24T12:59:30Z', value: 12288 }],
        temperatureCelsius: [{ timestamp: '2026-08-24T12:59:30Z', value: 54 }],
        powerWatts: [{ timestamp: '2026-08-24T12:59:30Z', value: 180 }],
      },
    },
    {
      uuid: 'GPU-2', nodeName: 'node-a', index: '1',
      series: {
        utilizationPercent: [
          { timestamp: '2026-08-24T12:59:00Z', value: 20 },
          { timestamp: '2026-08-24T12:59:30Z', value: 40 },
        ],
        memoryUsedMib: [{ timestamp: '2026-08-24T12:59:30Z', value: 4096 }],
        temperatureCelsius: [{ timestamp: '2026-08-24T12:59:30Z', value: 48 }],
        powerWatts: [{ timestamp: '2026-08-24T12:59:30Z', value: 120 }],
      },
    },
  ],
}

test('offers only the five server-approved GPU history windows', () => {
  assert.deepEqual(GPU_TIME_WINDOWS.map(({ value }) => value), ['15m', '1h', '6h', '24h', '7d'])
})

test('normalizes GPU history without mutating the API payload', () => {
  const original = structuredClone(historyPayload)
  const history = normalizeGPUHistory(historyPayload)
  assert.equal(history.devices[0].series.utilizationPercent[1].value, 100)
  assert.equal(history.devices[0].podName, 'job-1-worker')
  assert.deepEqual(historyPayload, original)
})

test('marks delayed samples instead of presenting missing data as zero', () => {
  assert.deepEqual(sampleFreshness('2026-08-24T12:59:30Z', new Date('2026-08-24T13:00:00Z')), { stale: false, ageSeconds: 30 })
  assert.deepEqual(sampleFreshness('2026-08-24T12:57:00Z', new Date('2026-08-24T13:00:00Z')), { stale: true, ageSeconds: 180 })
  assert.deepEqual(sampleFreshness('', new Date('2026-08-24T13:00:00Z')), { stale: true, ageSeconds: null })
})

test('summarizes recent node load and detects cross-GPU imbalance', () => {
  const summary = nodeMetricSummary(normalizeGPUHistory(historyPayload), 'node-a')
  assert.equal(summary.averageUtilizationPercent, 60)
  assert.equal(summary.totalMemoryUsedMib, 16384)
  assert.equal(summary.totalPowerWatts, 300)
  assert.equal(summary.maximumTemperatureCelsius, 54)
  assert.equal(summary.imbalanced, true)
  assert.equal(summary.utilizationSpread, 60)
})

test('builds one immutable chart series per physical GPU', () => {
  const history = normalizeGPUHistory(historyPayload)
  const series = metricChartSeries(history, 'node-a', 'utilizationPercent')
  assert.deepEqual(series.map((item) => item.name), ['GPU 0', 'GPU 1'])
  assert.deepEqual(series[0].data[1], ['2026-08-24T12:59:30.000Z', 100])
  series[0].data[0][1] = 0
  assert.equal(history.devices[0].series.utilizationPercent[0].value, 80)
})
