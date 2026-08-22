import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import { requestMLflowDashboardAccess } from './api/mlflowDashboard.js'

const experimentPage = new URL('./views/Experiments/index.vue', import.meta.url)

test('requests an authenticated MLflow access URL with an empty POST body', async () => {
  const calls = []

  const result = await requestMLflowDashboardAccess(async (path, body) => {
    calls.push([path, body])
    return { url: '/mlflow/?access_token=ticket_123' }
  })

  assert.deepEqual(calls, [['/api/v1/mlflow-dashboard-access', {}]])
  assert.equal(result, '/mlflow/?access_token=ticket_123')
})

test('accepts the standard API envelope without returning any metadata', async () => {
  const result = await requestMLflowDashboardAccess(async () => ({
    data: { url: '/mlflow/?access_token=enveloped_ticket' },
  }))

  assert.equal(result, '/mlflow/?access_token=enveloped_ticket')
})

test('rejects missing, malformed, absolute, and external MLflow access URLs', async () => {
  const invalidURLs = [
    undefined,
    '',
    '/mlflow/?access_token=',
    '/mlflow/?access_token=%20',
    '/mlflow/?access_token=ticket%0A',
    '/mlflow/?access_token=ticket&access_token=second',
    '/mlflow?access_token=ticket',
    '/mlflow/?access_token=%',
    'https://portal.example/mlflow/?access_token=ticket',
    '//external.example/mlflow/?access_token=ticket',
  ]

  for (const url of invalidURLs) {
    await assert.rejects(
      requestMLflowDashboardAccess(async () => ({ url })),
      /平台没有返回有效的 MLflow 访问地址/,
    )
  }
})

test('Experiment Center exposes the native MLflow dashboard with its global-access boundary copy', async () => {
  const source = await readFile(experimentPage, 'utf8')

  assert.match(source, /type="primary"[\s\S]*?>打开 MLflow 管理界面<\/el-button>/)
  assert.match(source, /所有已登录平台用户都可以查看和变更共享实验/)
  assert.match(source, /创建、修改、删除、模型注册表以及 MLflow Artifact 上传和下载均已启用/)
  assert.match(source, /不会开放公开训练数据下载/)
})

test('Experiment Center opens a protected blank tab synchronously and handles every popup outcome', async () => {
  const source = await readFile(experimentPage, 'utf8')
  const openIndex = source.indexOf("window.open('about:blank', '_blank', 'noopener,noreferrer')")
  const requestIndex = source.indexOf('await requestMLflowDashboardAccess()')

  assert.notEqual(openIndex, -1)
  assert.ok(requestIndex > openIndex, 'the blank tab must open before the asynchronous access request')
  assert.match(source, /if \(dashboardOpening\.value\) return/)
  assert.match(source, /:loading="dashboardOpening"/)
  assert.match(source, /popup\.opener = null/)
  assert.match(source, /popup\.location\.replace\(accessURL\)/)
  assert.match(source, /浏览器阻止了新标签页/)
  assert.match(source, /popup\.close\(\)[\s\S]*?ElMessage\.error/)
})
