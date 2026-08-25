import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const viewPath = new URL('./views/Job/JobDetail.vue', import.meta.url)

test('job metrics tab renders the training GPU summary and all four history charts', async () => {
  const source = await readFile(viewPath, 'utf8')

  for (const label of [
    '训练 GPU',
    '已分配 / 已观测 GPU',
    '近 1 分钟平均利用率',
    '显存使用量',
    '总功率',
    '最高温度',
    'GPU 使用率',
    'GPU 功率',
    'GPU 温度',
  ]) {
    assert.match(source, new RegExp(label))
  }

  assert.equal((source.match(/<GPUTrendChart\b/g) || []).length, 4)
  assert.doesNotMatch(source, /<MetricCard\b/)
  assert.match(source, /v-for="card in gpuSummaryCards"/)
})

test('job GPU UI reuses the bounded windows, API, normalization, summaries and freshness helpers', async () => {
  const source = await readFile(viewPath, 'utf8')

  for (const contract of [
    'GPUTrendChart',
    'fetchJobGPUHistory',
    'GPU_TIME_WINDOWS',
    'normalizeGPUHistory',
    'jobMetricSummary',
    'jobMetricChartSeries',
    'sampleFreshness',
  ]) {
    assert.match(source, new RegExp(contract))
  }

  assert.match(source, /selectedGPUWindow\s*=\s*ref\(['"]1h['"]\)/)
  assert.match(source, /v-for="item in GPU_TIME_WINDOWS"/)
  assert.match(source, /watch\(selectedGPUWindow,\s*refreshGPUHistory\)/)
})

test('job GPU UI explains empty, delayed, imbalanced and latest sample states without false zeroes', async () => {
  const source = await readFile(viewPath, 'utf8')

  for (const copy of ['排队', 'Worker 尚未启动', 'Prometheus', '保留期', '数据延迟', '负载不均', '最新样本']) {
    assert.match(source, new RegExp(copy))
  }

  assert.match(source, /gpuHasSamples/)
  assert.match(source, /gpuMetricHasSamples/)
  for (const metric of ['utilizationPercent', 'memoryUsedMib', 'powerWatts', 'temperatureCelsius']) {
    assert.match(source, new RegExp(`gpuMetricHasSamples\\('${metric}'\\) \\?`))
  }
  assert.match(source, /sampleFreshness\(gpuLatestSampleAt\.value/)
  assert.match(source, /gpuMetricSummary\?\.imbalanced/)
})

test('job GPU history has an independent 30-second lifecycle beside existing refresh loops', async () => {
  const source = await readFile(viewPath, 'utf8')

  assert.match(source, /gpuRefreshTimer\s*=\s*window\.setInterval\(refreshGPUHistory,\s*30000\)/)
  assert.match(source, /refreshTimer\s*=\s*window\.setInterval\(fetchDetail,\s*5000\)/)
  assert.match(source, /logRefreshTimer\s*=\s*window\.setInterval\(refreshLogs,\s*5000\)/)
  assert.match(source, /window\.clearInterval\(gpuRefreshTimer\)/)
})

test('job changes reset GPU state and stale requests cannot write into another job', async () => {
  const source = await readFile(viewPath, 'utf8')

  assert.match(source, /watch\(\(\) => route\.params\.id/)
  assert.match(source, /resetGPUState\(\)/)
  assert.match(source, /gpuHistory\.value\s*=\s*normalizeGPUHistory\(null\)/)
  assert.match(source, /requestJobId !== String\(route\.params\.id\)/)
  assert.match(source, /requestGeneration !== gpuRequestGeneration/)
})

test('failed GPU refreshes preserve the last successful history and expose retry copy', async () => {
  const source = await readFile(viewPath, 'utf8')
  const refreshFunction = source.match(/async function refreshGPUHistory\(\) \{[\s\S]*?\n\}/)?.[0] || ''
  const catchBlock = refreshFunction.match(/catch \(error\) \{[\s\S]*?\n  \}/)?.[0] || ''

  assert.match(refreshFunction, /gpuHistory\.value\s*=\s*normalizeGPUHistory\(payload\)/)
  assert.match(catchBlock, /gpuHistoryError\.value/)
  assert.doesNotMatch(catchBlock, /gpuHistory\.value\s*=/)
  assert.match(source, /已保留上一次成功数据/)
  assert.match(source, /30 秒后重试/)
})
