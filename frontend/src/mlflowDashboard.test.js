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
  const duplicateGuardIndex = source.indexOf('if (dashboardOpening.value) return')
  const openIndex = source.indexOf("window.open('about:blank', '_blank')")
  const blockedGuardIndex = source.indexOf('if (!popup)')
  const openerIndex = source.indexOf('popup.opener = null')
  const requestIndex = source.indexOf('await requestMLflowDashboardAccess()')
  const replaceIndex = source.indexOf('popup.location.replace(accessURL)')
  const closeIndex = source.indexOf('popup.close()')
  const errorIndex = source.indexOf('ElMessage.error')

  for (const [label, index] of Object.entries({
    duplicateGuardIndex,
    openIndex,
    blockedGuardIndex,
    openerIndex,
    requestIndex,
    replaceIndex,
    closeIndex,
    errorIndex,
  })) {
    assert.notEqual(index, -1, `missing popup contract marker: ${label}`)
  }
  assert.ok(duplicateGuardIndex < openIndex, 'duplicate requests must be rejected before opening another tab')
  assert.ok(blockedGuardIndex > openIndex, 'popup blocking must be checked immediately after opening')
  assert.ok(openerIndex > blockedGuardIndex && openerIndex < requestIndex, 'opener must be cleared before the first await')
  assert.ok(replaceIndex > requestIndex, 'the returned access URL must replace the blank tab history entry')
  assert.ok(closeIndex > requestIndex && closeIndex < errorIndex, 'a failed request must close the blank tab before reporting the error')
  assert.match(source, /:loading="dashboardOpening"/)
  assert.match(source, /浏览器阻止了新标签页/)
})
