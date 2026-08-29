import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import {
  jobRuntimeDetails,
  managedEngineAvailability,
  managedPolicyFromQuery,
  normalizeTrainingEngine,
  resubmitRuntimeQuery,
  trainingEngineLabel,
} from './trainingEngine.js'

const managedImage = {
  reference: 'registry.example/ray@sha256:' + 'a'.repeat(64),
  rayVersion: '2.56.1',
  supportedEngines: ['ray-ddp', 'ray-train'],
}

test('missing and legacy jobs resolve to Ray orchestrated DDP without consulting execution.mode', () => {
  assert.equal(normalizeTrainingEngine(), 'ray-ddp')
  assert.equal(normalizeTrainingEngine('unknown'), 'ray-ddp')
  assert.equal(trainingEngineLabel({ execution: { mode: 'ray_train' } }), 'Ray 编排 DDP')
  assert.equal(trainingEngineLabel({ trainingEngine: 'ray-train', execution: { mode: 'ray_train' } }), 'Ray Train 托管')
})

test('managed availability requires both caller capability and selected image compatibility', () => {
  const limits = { runtime: { managedEnabled: true, availableEngines: ['ray-ddp', 'ray-train'] } }

  assert.deepEqual(
    managedEngineAvailability({ limits, images: [managedImage], imageReference: managedImage.reference }),
    { available: true, reason: '' },
  )
  assert.match(
    managedEngineAvailability({ limits: {}, images: [managedImage], imageReference: managedImage.reference }).reason,
    /未开放/,
  )
  assert.match(
    managedEngineAvailability({
      limits,
      images: [{ ...managedImage, supportedEngines: ['ray-ddp'] }],
      imageReference: managedImage.reference,
    }).reason,
    /镜像.*不支持/,
  )
})

test('managed query normalization owns a fresh result and never mutates caller state', () => {
  const query = Object.freeze({ maxFailures: '7', checkpointKeepLatest: '9' })
  const normalized = managedPolicyFromQuery(query)

  assert.deepEqual(query, { maxFailures: '7', checkpointKeepLatest: '9' })
  assert.deepEqual(normalized, {
    maxFailures: 7,
    checkpointEveryEpochs: 1,
    checkpointKeepLatest: 9,
    checkpointKeepBest: 1,
  })
  assert.notEqual(normalized, query)
})

test('rerun preserves the explicit immutable engine and resume creates a child relationship', () => {
  const parentJobId = 'job-0123456789abcdef01234567'
  const job = {
    id: parentJobId,
    spec: {
      trainingEngine: 'ray-train',
      managed: { maxFailures: 3, checkpoint: { everyEpochs: 2, keepLatest: 4, keepBest: 1 } },
    },
  }

  assert.deepEqual(resubmitRuntimeQuery(job), {
    trainingEngine: 'ray-train',
    maxFailures: '3',
    checkpointEveryEpochs: '2',
    checkpointKeepLatest: '4',
    checkpointKeepBest: '1',
  })
  assert.deepEqual(resubmitRuntimeQuery(job, { resume: true }), {
    trainingEngine: 'ray-train',
    maxFailures: '3',
    checkpointEveryEpochs: '2',
    checkpointKeepLatest: '4',
    checkpointKeepBest: '1',
    parentJobId,
  })
})

test('job runtime details expose immutable runtime and recovery topology fields', () => {
  assert.deepEqual(jobRuntimeDetails({
    clusterAttempt: 2,
    workerRestartCount: 1,
    resumeCheckpointId: 'checkpoint-17',
    spec: {
      trainingEngine: 'ray-train',
      rayVersion: '2.56.1',
      image: managedImage.reference,
      parentJobId: 'job-0123456789abcdef01234567',
      resources: { workerReplicas: 2, gpusPerWorker: 8 },
    },
  }), {
    engine: 'ray-train',
    engineLabel: 'Ray Train 托管',
    rayVersion: '2.56.1',
    image: managedImage.reference,
    workerCount: 2,
    worldSize: 16,
    clusterAttempt: 2,
    restartCount: 1,
    resumeSource: 'checkpoint-17',
  })
})

test('runtime UI renders both accessible engine cards, managed policy controls, and immutable details', async () => {
  const [runtime, preview, detail] = await Promise.all([
    readFile(new URL('./components/job/StepRuntime.vue', import.meta.url), 'utf8'),
    readFile(new URL('./components/job/SubmitPreview.vue', import.meta.url), 'utf8'),
    readFile(new URL('./views/Job/JobDetail.vue', import.meta.url), 'utf8'),
  ])

  assert.match(runtime, /Ray 编排 DDP/)
  assert.match(runtime, /Ray Train 托管/)
  assert.match(runtime, /aria-pressed/)
  assert.match(runtime, /aria-disabled/)
  assert.match(runtime, /:disabled="!managedAvailability\.available"/)
  assert.match(runtime, /<el-alert[\s\S]*managedAvailability\.reason/)
  assert.match(runtime, /managedAvailability\.reason/)
  assert.match(runtime, /maxFailures/)
  assert.match(runtime, /checkpointEveryEpochs/)
  assert.match(preview, /训练引擎/)
  for (const label of ['Ray 版本', '镜像 Digest / 引用', 'Worker 数', 'World Size', '集群尝试', 'Worker 重启', '续训来源']) {
    assert.match(detail, new RegExp(label))
  }
})
