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

test('job GPU UI wires bounded windows, chart helpers and the behavioral history controller', async () => {
  const source = await readFile(viewPath, 'utf8')

  for (const contract of [
    'GPUTrendChart',
    'fetchJobGPUHistory',
    'createJobGPUHistoryController',
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
})

test('job GPU UI explains empty, delayed, imbalanced, partial coverage and latest sample states', async () => {
  const source = await readFile(viewPath, 'utf8')

  for (const copy of ['排队', 'Worker 尚未启动', 'Prometheus', '保留期', '数据延迟', '负载不均', '最新样本', '卡有样本']) {
    assert.match(source, new RegExp(copy))
  }

  assert.match(source, /gpuHasSamples/)
  assert.match(source, /gpuCoverageHint/)
  assert.match(source, /sampleFreshness\(gpuLatestSampleAt\.value/)
  assert.match(source, /gpuMetricSummary\?\.imbalanced/)
  assert.match(source, /value:\s*summary\.averageUtilizationPercent\s*==\s*null\s*\?\s*['"]—['"]/)
})
