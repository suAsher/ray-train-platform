import assert from 'node:assert/strict'
import test from 'node:test'

import { dataSpaceReadiness } from './dataSpaceReadiness.js'

test('data-space readiness requires a bound mount for every workload data source', () => {
  assert.deepEqual(dataSpaceReadiness({ provider: 'tos', storageStatus: 'ready', mountStatus: 'ready' }), { ready: true, message: '' })
  assert.deepEqual(dataSpaceReadiness({ provider: 'tos', storageStatus: 'ready', mountStatus: 'pending' }), {
    ready: false,
    message: 'GPU 数据挂载尚未启用；你可以浏览文件，管理员完成存储挂载验收后才可用于调试和训练。',
  })
  assert.deepEqual(dataSpaceReadiness({ provider: 'tos', storageStatus: 'ready', mountStatus: 'failed' }), {
    ready: false,
    message: 'GPU 挂载配置失败，请联系平台管理员处理。',
  })
  assert.deepEqual(dataSpaceReadiness({ provider: 'idc', mountStatus: 'not-configured' }), {
    ready: false,
    message: '管理员尚未为此 IDC 数据登记只读挂载。',
  })
})

test('data-space readiness distinguishes an available object space from an unavailable one', () => {
  assert.deepEqual(dataSpaceReadiness({ provider: 'tos', storageStatus: 'not-configured', mountStatus: 'not-configured' }), {
    ready: false,
    message: '个人对象空间尚未就绪，请联系平台管理员处理。',
  })
})

test('a ready legacy TOS mount implies object storage only when storageStatus is omitted', () => {
  assert.deepEqual(dataSpaceReadiness({ provider: 'tos', mountStatus: 'ready' }), { ready: true, message: '' })
  assert.deepEqual(dataSpaceReadiness({ provider: 'tos', storageStatus: 'not-configured', mountStatus: 'ready' }), {
    ready: false,
    message: '个人对象空间尚未就绪，请联系平台管理员处理。',
  })
})
