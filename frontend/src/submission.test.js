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
