import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const viewPath = new URL('./views/DevicesManagement/index.vue', import.meta.url)
const chartPath = new URL('./components/gpu/GPUTrendChart.vue', import.meta.url)
const apiPath = new URL('./api/platform.js', import.meta.url)

test('GPU resource page explains current, averaged and delayed samples', async () => {
  const source = await readFile(viewPath, 'utf8')
  for (const copy of ['近 1 分钟平均', '最新采样', '数据延迟', '负载不均', '显存占比', '所属工作负载']) {
    assert.match(source, new RegExp(copy))
  }
})

test('GPU resource page renders all four node trend metrics with bounded windows', async () => {
  const source = await readFile(viewPath, 'utf8')
  assert.match(source, /GPUTrendChart/)
  for (const label of ['GPU 使用率', '显存使用量', 'GPU 功率', 'GPU 温度']) {
    assert.match(source, new RegExp(label))
  }
  assert.match(source, /GPU_TIME_WINDOWS/)
  assert.match(source, /fetchGPUHistory/)
	assert.match(source, /fetchGPUHistory\(selectedWindow\.value\)/)
})

test('trend chart preserves missing samples and supports synchronized inspection', async () => {
  const source = await readFile(chartPath, 'utf8')
  assert.match(source, /connectNulls:\s*false/)
  assert.match(source, /trigger:\s*'axis'/)
  assert.match(source, /ResizeObserver/)
})

test('frontend history API sends only window and optional node parameters', async () => {
  const source = await readFile(apiPath, 'utf8')
  assert.match(source, /gpu-metrics\/history/)
  assert.match(source, /URLSearchParams/)
  assert.doesNotMatch(source, /promql|queryExpression/)
})
