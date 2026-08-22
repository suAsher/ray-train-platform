import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import {
  fetchExperimentCatalog,
  formatExperimentDuration,
  formatExperimentTime,
  formatMetricValue,
  keyMetrics,
  normalizeExperimentCatalog,
  statusPresentation,
} from './experimentCatalog.js'

const catalogItem = {
  experimentName: 'raytrain-team-a',
  jobId: 'job-01',
  jobName: 'bevfusion-baseline',
  run: {
    id: 'run-01',
    name: 'nightly-run',
    status: 'FINISHED',
    startTimeMs: 1_000,
    endTimeMs: 66_000,
    latest: {
      train_loss: 0.25,
      learning_rate: 0.0005,
      epoch: 4,
      mAP: 0.67,
      NDS: 0.72,
    },
  },
}

test('fetches the authenticated experiment catalog from the platform API', async () => {
  const requestedPaths = []
  const result = await fetchExperimentCatalog(async (path) => {
    requestedPaths.push(path)
    return { items: [catalogItem] }
  })

  assert.deepEqual(requestedPaths, ['/api/v1/experiments'])
  assert.equal(result.length, 1)
  assert.equal(result[0].jobId, 'job-01')
})

test('normalizes run, job, timing, and latest metric fields for presentation', () => {
  const [experiment] = normalizeExperimentCatalog({ items: [catalogItem] })

  assert.deepEqual(
    {
      experimentName: experiment.experimentName,
      jobId: experiment.jobId,
      jobName: experiment.jobName,
      runId: experiment.runId,
      runName: experiment.runName,
      status: experiment.status,
      startTimeMs: experiment.startTimeMs,
      endTimeMs: experiment.endTimeMs,
    },
    {
      experimentName: 'raytrain-team-a',
      jobId: 'job-01',
      jobName: 'bevfusion-baseline',
      runId: 'run-01',
      runName: 'nightly-run',
      status: 'FINISHED',
      startTimeMs: 1_000,
      endTimeMs: 66_000,
    },
  )
  assert.deepEqual(
    experiment.metrics.map(({ label, value }) => ({ label, value })),
    [
      { label: 'Loss', value: 0.25 },
      { label: '学习率', value: 0.0005 },
      { label: 'Epoch', value: 4 },
      { label: 'mAP', value: 0.67 },
      { label: 'NDS', value: 0.72 },
    ],
  )
})

test('normalizes the backend catalog envelope and carries its experiment name to each run', () => {
  const [experiment] = normalizeExperimentCatalog({
    experimentName: 'raytrain-team-a',
    runs: [{
      id: 'run-from-api',
      name: 'api-run',
      status: 'RUNNING',
      jobId: 'job-from-api',
      startTimeMs: 20_000,
      latest: { loss: 0.75 },
    }],
  })

  assert.equal(experiment.experimentName, 'raytrain-team-a')
  assert.equal(experiment.runId, 'run-from-api')
  assert.equal(experiment.jobId, 'job-from-api')
})

test('accepts a bare list and uses MLflow params as safe job fallbacks', () => {
  const [experiment] = normalizeExperimentCatalog([{
    id: 'run-flat',
    status: 'RUNNING',
    startTimeMs: 10_000,
    latest: { loss: '1.5', lr: 0 },
    params: {
      'platform.job_id': 'job-from-param',
      'platform.job_name': 'fallback-name',
    },
  }])

  assert.equal(experiment.jobId, 'job-from-param')
  assert.equal(experiment.jobName, 'fallback-name')
  assert.deepEqual(experiment.metrics.map(({ value }) => value), [1.5, 0])
})

test('presents known and unknown MLflow statuses without guessing', () => {
  assert.deepEqual(statusPresentation('RUNNING'), { label: '运行中', type: 'success' })
  assert.deepEqual(statusPresentation('FINISHED'), { label: '已完成', type: 'success' })
  assert.deepEqual(statusPresentation('FAILED'), { label: '失败', type: 'danger' })
  assert.deepEqual(statusPresentation('KILLED'), { label: '已终止', type: 'warning' })
  assert.deepEqual(statusPresentation('paused'), { label: 'paused', type: 'info' })
  assert.deepEqual(statusPresentation(), { label: '未知', type: 'info' })
})

test('formats completed and active run durations and rejects invalid ranges', () => {
  assert.equal(formatExperimentDuration(1_000, 66_000), '1 分 05 秒')
  assert.equal(formatExperimentDuration(1_000, null, 3_601_000), '1 时 00 分')
  assert.equal(formatExperimentDuration(null, 66_000), '—')
  assert.equal(formatExperimentDuration(66_000, 1_000), '—')
})

test('formats catalog timestamps and metric values without misleading placeholders', () => {
  assert.notEqual(formatExperimentTime('1000'), '—')
  assert.equal(formatExperimentTime('not-a-date'), '—')
  assert.equal(formatMetricValue(null), '—')
  assert.equal(formatMetricValue(4), '4')
  assert.equal(formatMetricValue(0.00005), '5.00e-5')
  assert.equal(formatMetricValue(0.125), '0.125')
  assert.equal(formatMetricValue(12.3456), '12.346')
})

test('treats missing or malformed catalog envelopes as empty', () => {
  assert.deepEqual(normalizeExperimentCatalog(null), [])
  assert.deepEqual(normalizeExperimentCatalog({ data: { items: [] } }), [])
  assert.deepEqual(normalizeExperimentCatalog({ data: null }), [])
})

test('selects only finite key metrics in a stable display order', () => {
  assert.deepEqual(
    keyMetrics({ nds: 0.7, mean_average_precision: 0.6, loss: Number.NaN, current_epoch: 8 }),
    [
      { key: 'epoch', label: 'Epoch', value: 8 },
      { key: 'map', label: 'mAP', value: 0.6 },
      { key: 'nds', label: 'NDS', value: 0.7 },
    ],
  )
})

test('registers a persistent authenticated route, navigation entry, and job detail link', async () => {
  const [routerSource, layoutSource, viewSource] = await Promise.all([
    readFile(new URL('./router/index.js', import.meta.url), 'utf8'),
    readFile(new URL('./layout/Layout.vue', import.meta.url), 'utf8'),
    readFile(new URL('./views/Experiments/index.vue', import.meta.url), 'utf8'),
  ])

  assert.match(routerSource, /path:\s*['"]experiments['"]/)
  assert.match(routerSource, /name:\s*['"]Experiments['"][\s\S]*?requiresAuth:\s*true/)
  assert.match(layoutSource, /to:\s*['"]\/experiments['"][\s\S]*?实验中心/)
  assert.match(viewSource, /name:\s*['"]JobDetail['"]/)
})
