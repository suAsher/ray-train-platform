import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import test from 'node:test'

import * as gpuMetrics from './gpuMetrics.js'

const historyPayload = {
  window: '1h',
  devices: [
    {
      uuid: 'GPU-b10', nodeName: 'node-b', index: '10',
      series: {
        utilizationPercent: [{ timestamp: '2026-08-24T12:59:30Z', value: 50 }],
        memoryUsedMib: [{ timestamp: '2026-08-24T12:59:30Z', value: 2048 }],
        temperatureCelsius: [{ timestamp: '2026-08-24T12:59:30Z', value: 70 }],
        powerWatts: [{ timestamp: '2026-08-24T12:59:30Z', value: 150 }],
      },
    },
    {
      uuid: 'GPU-a2', nodeName: 'node-a', index: '2',
      series: {
        utilizationPercent: [{ timestamp: '2026-08-24T12:59:30Z', value: 20 }],
        memoryUsedMib: [{ timestamp: '2026-08-24T12:59:30Z', value: 4096 }],
        temperatureCelsius: [{ timestamp: '2026-08-24T12:59:30Z', value: 48 }],
        powerWatts: [{ timestamp: '2026-08-24T12:59:30Z', value: 120 }],
      },
    },
    {
      uuid: 'GPU-a0', nodeName: 'node-a', index: '0',
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
  ],
}

test('builds an encoded job GPU history path with only approved windows', () => {
  assert.equal(typeof gpuMetrics.jobGPUHistoryPath, 'function')
  assert.equal(
    gpuMetrics.jobGPUHistoryPath('job /中文?#', '24h'),
    '/api/v1/jobs/job%20%2F%E4%B8%AD%E6%96%87%3F%23/gpu-metrics?window=24h',
  )
  assert.equal(
    gpuMetrics.jobGPUHistoryPath('job-a', '24h&node=node-a'),
    '/api/v1/jobs/job-a/gpu-metrics?window=1h',
  )
  assert.equal(gpuMetrics.jobGPUHistoryPath('job-a'), '/api/v1/jobs/job-a/gpu-metrics?window=1h')
})

test('summarizes every GPU in a multi-node job and detects imbalance', () => {
  assert.equal(typeof gpuMetrics.jobMetricSummary, 'function')
  const summary = gpuMetrics.jobMetricSummary(gpuMetrics.normalizeGPUHistory(historyPayload))

  assert.deepEqual(summary, {
    deviceCount: 3,
    averageUtilizationPercent: 53,
    totalMemoryUsedMib: 18432,
    totalPowerWatts: 450,
    maximumTemperatureCelsius: 70,
    utilizationSpread: 70,
    imbalanced: true,
  })
})

test('builds immutable job chart series sorted by node and numeric GPU index', () => {
  assert.equal(typeof gpuMetrics.jobMetricChartSeries, 'function')
  const history = gpuMetrics.normalizeGPUHistory(historyPayload)
  const original = structuredClone(history)
  const series = gpuMetrics.jobMetricChartSeries(history, 'utilizationPercent')

  assert.deepEqual(series.map(({ name }) => name), [
    'node-a / GPU 0',
    'node-a / GPU 2',
    'node-b / GPU 10',
  ])
  assert.deepEqual(history, original)

  series[0].data[0][1] = -1
  assert.equal(history.devices[2].series.utilizationPercent[0].value, 80)
})

test('orders non-numeric and numerically equivalent GPU indexes deterministically', () => {
  const history = gpuMetrics.normalizeGPUHistory({
    devices: [
      { uuid: 'GPU-slot-b', nodeName: 'node-a', index: ' slot-b ' },
      { uuid: 'GPU-z', nodeName: 'node-a', index: '2' },
      { uuid: 'GPU-a', nodeName: 'node-a', index: '02' },
      { uuid: 'GPU-slot-a', nodeName: 'node-a', index: 'slot-a' },
    ],
  })
  const original = structuredClone(history)

  const series = gpuMetrics.jobMetricChartSeries(history, 'utilizationPercent')

  assert.deepEqual(series.map(({ id }) => id), ['GPU-a', 'GPU-z', 'GPU-slot-a', 'GPU-slot-b'])
  assert.deepEqual(history, original)
})

test('labels GPUs without a node clearly and rejects unsupported metrics', () => {
  const history = gpuMetrics.normalizeGPUHistory({
    devices: [{
      uuid: 'GPU-unknown', index: '3',
      series: { memoryUsedMib: [{ timestamp: '2026-08-24T12:59:30Z', value: 1024 }] },
    }],
  })

  assert.equal(gpuMetrics.jobMetricChartSeries(history, 'memoryUsedMib')[0].name, '未知节点 / GPU 3')
  assert.deepEqual(gpuMetrics.jobMetricChartSeries(history, 'madeUpMetric'), [])
})

test('job GPU API wrapper delegates only the bounded same-origin path', () => {
  const apiModuleUrl = new URL('./api/jobGpuMetrics.js', import.meta.url)
  assert.equal(existsSync(apiModuleUrl), true)

  const source = readFileSync(apiModuleUrl, 'utf8')
  assert.match(source, /import \{ apiGet \} from '\.\/client\.js'/)
  assert.match(source, /import \{ jobGPUHistoryPath \} from '\.\.\/gpuMetrics\.js'/)
  assert.match(source, /function fetchJobGPUHistory\(jobId, window\)/)
  assert.match(source, /return apiGet\(jobGPUHistoryPath\(jobId, window\)\)/)
  assert.doesNotMatch(source, /\b(?:node|namespace|pod|regex|queryExpression|promql)\b/i)
})
