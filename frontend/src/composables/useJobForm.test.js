import assert from 'node:assert/strict'
import test from 'node:test'

import { jobFormStepIssues } from './jobFormIssues.js'
import { buildJobSpec } from '../submission.js'
import { useJobForm } from './useJobForm.js'

const managedImage = {
  reference: 'registry.example/ray@sha256:' + 'a'.repeat(64),
  supportedEngines: ['ray-ddp', 'ray-train'],
}

const makeManagedAvailable = (state) => {
  state.limits.value = {
    ...state.limits.value,
    maxWorkerReplicas: 2,
    maxGpusPerWorker: 8,
    maxTotalGpus: 16,
    tenantQuota: { gpuLimit: 16, gpuUsed: 0, gpuAvailable: 16 },
    runtime: { managedEnabled: true, availableEngines: ['ray-ddp', 'ray-train'] },
  }
  state.trainingImages.value = [managedImage]
  state.form.image = managedImage.reference
}

test('job form defaults managed recovery values and sanitizes invalid copied query policy', () => {
  const defaults = useJobForm({ query: {} })
  assert.equal(defaults.form.trainingEngine, 'ray-ddp')
  assert.equal(defaults.form.dataMode, 'mount')
  assert.deepEqual(defaults.form.datasetRef, { dataset: '', version: 'latest' })
  assert.equal(defaults.form.datasetCachePolicy, 'auto')
  assert.deepEqual([
    defaults.form.maxFailures,
    defaults.form.checkpointEveryEpochs,
    defaults.form.checkpointKeepLatest,
    defaults.form.checkpointKeepBest,
  ], [2, 1, 3, 1])

  const copied = useJobForm({ query: {
    trainingEngine: 'ray-train',
    maxFailures: '11',
    checkpointEveryEpochs: '100001',
    checkpointKeepLatest: '1001',
    checkpointKeepBest: '-1',
    parentJobId: 'job-0123456789abcdef01234567',
  } })
  assert.equal(copied.form.trainingEngine, 'ray-train')
  assert.deepEqual([
    copied.form.maxFailures,
    copied.form.checkpointEveryEpochs,
    copied.form.checkpointKeepLatest,
    copied.form.checkpointKeepBest,
  ], [2, 1, 3, 1])
  assert.equal(copied.form.parentJobId, 'job-0123456789abcdef01234567')
})

test('streaming query is fail-closed until limits and a Ray 2.58 image authorize it', async () => {
  const image = { ...managedImage, rayVersion: '2.58.0' }
  const legacyDefault = { ...managedImage, reference: 'registry.example/ray:2.56', rayVersion: '2.56.1', isDefault: true }
  const enabledLoaders = {
    fetchPlatformLimits: async () => ({
      maxWorkerReplicas: 2,
      maxGpusPerWorker: 8,
      maxTotalGpus: 16,
      datasets: { versioningEnabled: true, streamingEnabled: true, publisherEnabled: true },
      runtime: {
        managedEnabled: true,
        canaryEnabled: true,
        canaryRayVersion: '2.58.0',
        availableEngines: ['ray-ddp', 'ray-train'],
      },
    }),
    fetchImages: async () => [legacyDefault, image],
    fetchWorkspaceSnapshots: async () => [],
  }
  const enabled = useJobForm({ query: {
    trainingEngine: 'ray-train', dataMode: 'streaming', dataset: 'labeled-full', datasetVersion: 'latest', cachePolicy: 'bounded',
  } }, enabledLoaders)
  await enabled.loadCatalog()

  assert.equal(enabled.form.dataMode, 'streaming')
  assert.deepEqual(enabled.form.datasetRef, { dataset: 'labeled-full', version: 'latest' })
  assert.equal(enabled.form.datasetCachePolicy, 'bounded')
  assert.equal(enabled.form.image, image.reference)
  assert.equal(enabled.datasetCapabilities.value.streamingEnabled, true)
  assert.equal(enabled.streamingAvailability.value.available, true)

  const disabled = useJobForm({ query: { trainingEngine: 'ray-train', dataMode: 'streaming' } }, {
    ...enabledLoaders,
    fetchPlatformLimits: async () => ({ datasets: { versioningEnabled: false, streamingEnabled: false } }),
  })
  await disabled.loadCatalog()
  assert.equal(disabled.form.dataMode, 'mount')
  assert.equal(disabled.streamingAvailability.value.available, false)
})

test('streaming data selection is validated in the data step before preflight', () => {
  const state = useJobForm({ query: { trainingEngine: 'ray-train', dataMode: 'streaming' } })
  state.limits.value = {
    ...state.limits.value,
    datasets: { versioningEnabled: true, streamingEnabled: true, publisherEnabled: true },
    runtime: { managedEnabled: true, canaryEnabled: true, canaryRayVersion: '2.58.0', availableEngines: ['ray-ddp', 'ray-train'] },
  }
  const image = { ...managedImage, rayVersion: '2.58.0' }
  state.trainingImages.value = [image]
  state.form.image = image.reference

  assert.match(state.stepIssues(2).join('\n'), /请选择数据集/)
  state.form.datasetRef = { dataset: 'labeled-full', version: 'latest' }
  assert.deepEqual(state.stepIssues(2), [])
})

test('managed policy mutation blocks the runtime step and submission at every UI boundary', () => {
  const state = useJobForm({ query: { trainingEngine: 'ray-train' } })
  makeManagedAvailable(state)
  Object.assign(state.form, {
    name: 'managed-test',
    codeSourceType: 'git',
    gitURL: 'https://git.example.com/team/train.git',
    gitCommit: '0123456789abcdef',
    workerReplicas: 2,
    gpusPerWorker: 1,
  })

  state.form.maxFailures = 11
  assert.match(state.stepIssues(1).join('\n'), /最大恢复次数.*0.*10/)
  assert.throws(() => buildJobSpec(state.toSubmission()), /最大恢复次数.*0.*10/)

  state.form.maxFailures = 2
  state.form.checkpointEveryEpochs = 100001
  assert.match(state.stepIssues(1).join('\n'), /Checkpoint.*周期.*100000/)
  assert.throws(() => buildJobSpec(state.toSubmission()), /Checkpoint.*周期.*100000/)

  state.form.checkpointEveryEpochs = 1
  state.form.checkpointKeepLatest = null
  assert.match(state.stepIssues(1).join('\n'), /最近 Checkpoint.*非负整数/)
  assert.throws(() => buildJobSpec(state.toSubmission()), /最近 Checkpoint.*非负整数/)
})

test('managed selection survives an incompatible image transition and becomes visibly blocking', () => {
  const state = useJobForm({ query: { trainingEngine: 'ray-train' } })
  makeManagedAvailable(state)
  assert.equal(state.managedAvailability.value.available, true)
  assert.doesNotMatch(state.stepIssues(1).join('\n'), /不支持 Ray Train/)

  state.trainingImages.value = [{ ...managedImage, supportedEngines: ['ray-ddp'] }]
  assert.equal(state.form.trainingEngine, 'ray-train')
  assert.equal(state.managedAvailability.value.available, false)
  assert.match(state.stepIssues(1).join('\n'), /镜像.*不支持 Ray Train/)
})

test('runtime step blocks submission with a clear Chinese message when team quota is exhausted', () => {
  const issues = jobFormStepIssues({
    step: 1,
    form: { entrypoint: 'python train.py', workerReplicas: 1, gpusPerWorker: 1 },
    limits: {
      maxWorkerReplicas: 0,
      maxGpusPerWorker: 0,
      maxTotalGpus: 0,
      tenantQuota: { gpuLimit: 8, gpuUsed: 8, gpuAvailable: 0 },
    },
    commandWarnings: [],
  })

  assert.deepEqual(issues, ['团队当前没有可用 GPU 配额，请等待正在运行的任务释放资源，或联系超级管理员调整配额'])
})

test('runtime step remains valid when newly fetched limits have available GPUs', () => {
  const issues = jobFormStepIssues({
    step: 1,
    form: { entrypoint: 'python train.py', workerReplicas: 3, gpusPerWorker: 8 },
    limits: {
      maxWorkerReplicas: 4,
      maxGpusPerWorker: 8,
      maxTotalGpus: 32,
      tenantQuota: { gpuLimit: 32, gpuUsed: 8, gpuAvailable: 24 },
    },
    commandWarnings: [],
  })

  assert.deepEqual(issues, [])
})
