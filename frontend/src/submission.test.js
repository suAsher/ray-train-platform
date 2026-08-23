import assert from 'node:assert/strict'
import test from 'node:test'

import { buildJobSpec, parseEntrypoint } from './submission.js'

const baseForm = () => ({
  name: 'support-sft-001',
  image: 'registry.example/ray@sha256:' + 'a'.repeat(64),
  codeSourceType: 'git',
  gitURL: 'https://git.example.com/ml/support-sft.git',
  gitCommit: '0123456789abcdef',
  workspaceSnapshot: '',
  tosCodeURI: '',
  entrypoint: 'python train.py --epochs 3',
  input: { spaceId: 'team-shared', relativePath: 'train' },
  checkpoint: { spaceId: 'public', relativePath: 'models/base' },
  output: { spaceId: 'my-runs' },
  workerReplicas: 2,
  gpusPerWorker: 8,
  cpuPerWorker: 32,
  memoryPerWorker: '128Gi',
  timeoutSeconds: 3600,
  maxRetries: 1,
})

const cacheLimits = (overrides = {}) => ({
  cache: {
    enabled: true,
    defaultMode: 'off',
    modes: ['off', 'runtime'],
    allowedSizes: ['100Gi', '200Gi'],
    defaultSize: '200Gi',
    maxSize: '200Gi',
    mountPath: '/mnt/cache',
    ...overrides,
  },
})

test('parseEntrypoint preserves quoted arguments', () => {
  assert.deepEqual(
    parseEntrypoint('python train.py --run-name "support sft"'),
    ['python', 'train.py', '--run-name', 'support sft'],
  )
})

test('buildJobSpec maps a Git submission into the platform runtime contract', () => {
  const spec = buildJobSpec(baseForm())

  assert.deepEqual(spec.source, {
    type: 'git',
    url: 'https://git.example.com/ml/support-sft.git',
    commit: '0123456789abcdef',
  })
  assert.deepEqual(spec.entrypoint, {
    command: ['python'],
    args: ['train.py', '--epochs', '3'],
  })
  assert.deepEqual(spec.resources, {
    workerReplicas: 2,
    gpusPerWorker: 8,
    cpuPerWorker: 32,
    memoryPerWorker: '128Gi',
  })

  assert.deepEqual(spec.execution, { mode: 'ray_train' })
  assert.deepEqual(spec.input, { space: 'team-shared', relativePath: 'train' })
  assert.deepEqual(spec.checkpoint, { space: 'public', relativePath: 'models/base' })
  assert.deepEqual(spec.output, { space: 'my-runs' })
  assert.equal('datasetStorage' in spec, false)
  assert.equal('datasetUri' in spec, false)
  assert.equal(spec.timeoutSeconds, 3600)
  assert.deepEqual(spec.retryPolicy, { maxRetries: 1 })
})

test('buildJobSpec rejects a multi-worker torchrun request before submitting it', () => {
  const form = baseForm()
  form.executionMode = 'torchrun'
  assert.throws(() => buildJobSpec(form), /单机多卡 DDP/)
})

test('buildJobSpec maps a workspace snapshot and rejects unsafe logical data paths', () => {
  const form = baseForm()
  form.codeSourceType = 'workspace'
  form.workspaceSnapshot = 'snapshot-20260811'
  form.input = { spaceId: 'team-shared', relativePath: '../private' }

  assert.throws(() => buildJobSpec(form), /训练输入/)
  form.input = { spaceId: 'team-shared', relativePath: 'validation' }
  const spec = buildJobSpec(form)

  assert.deepEqual(spec.source, { type: 'workspace', snapshot: 'snapshot-20260811' })
  assert.deepEqual(spec.input, { space: 'team-shared', relativePath: 'validation' })
})

test('buildJobSpec rejects retired object-store source types', () => {
	const form = baseForm()
	form.codeSourceType = 'tos'
	assert.throws(() => buildJobSpec(form), /Git 仓库或调试工作区代码版本/)
})

test('buildJobSpec always lets the platform allocate a task output directory', () => {
  const form = baseForm()
  form.output = { spaceId: 'team-shared' }
  assert.throws(() => buildJobSpec(form), /我的训练结果/)
})

test('buildJobSpec keeps omitted and explicit-off cache backward compatible', () => {
  const omitted = buildJobSpec(baseForm(), cacheLimits())
  assert.equal('cache' in omitted, false)

  const off = buildJobSpec({ ...baseForm(), cacheMode: 'off', cacheSize: '' }, cacheLimits())
  assert.equal('cache' in off, false)
})

test('buildJobSpec maps a policy-approved runtime cache', () => {
  const spec = buildJobSpec({ ...baseForm(), cacheMode: 'runtime', cacheSize: '200Gi' }, cacheLimits())
  assert.deepEqual(spec.cache, { mode: 'runtime', size: '200Gi' })
})

test('buildJobSpec rejects unsupported or internally inconsistent cache requests', () => {
  assert.throws(
    () => buildJobSpec({ ...baseForm(), cacheMode: 'persistent', cacheSize: '200Gi' }, cacheLimits()),
    /缓存模式/,
  )
  assert.throws(
    () => buildJobSpec({ ...baseForm(), cacheMode: 'off', cacheSize: '200Gi' }, cacheLimits()),
    /缓存关闭时不能选择容量/,
  )
  assert.throws(
    () => buildJobSpec({ ...baseForm(), cacheMode: 'runtime', cacheSize: '' }, cacheLimits()),
    /请选择运行时缓存容量/,
  )
})

test('buildJobSpec rejects runtime cache disabled or disallowed by the loaded server policy', () => {
  const form = { ...baseForm(), cacheMode: 'runtime', cacheSize: '200Gi' }
  assert.throws(() => buildJobSpec(form, cacheLimits({ enabled: false })), /未开放运行时缓存/)
  assert.throws(() => buildJobSpec(form, cacheLimits({ modes: ['off'] })), /未开放运行时缓存/)
  assert.throws(() => buildJobSpec(form, cacheLimits({ allowedSizes: ['100Gi'] })), /不在平台允许范围/)
})
