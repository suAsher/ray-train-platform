import assert from 'node:assert/strict'
import test from 'node:test'

import * as platformLimits from './platformLimits.js'

const {
  adminQuotaModel,
  cacheQueryForJob,
  clampResources,
  defaultPlatformLimits,
  jobQuotaModel,
  normalizeCachePolicy,
  normalizeCacheSelection,
  profilesFromLimits,
  resolveExecutionMode,
} = platformLimits

const limits = {
  maxWorkerReplicas: 2,
  maxGpusPerWorker: 8,
  maxTotalGpus: 16,
  mountPaths: { workspace: '/workspace', dataset: '/mnt/data/input', checkpoint: '/mnt/data/checkpoints', output: '/mnt/data/output' },
  executionProfiles: [
    { mode: 'single_gpu', name: '单卡', minWorkerReplicas: 1, maxWorkerReplicas: 1, minGpusPerWorker: 1, available: true },
    { mode: 'torchrun', name: '单机多卡 DDP', minWorkerReplicas: 1, maxWorkerReplicas: 1, minGpusPerWorker: 2, available: true },
    { mode: 'ray_train', name: '多机多卡', minWorkerReplicas: 2, maxWorkerReplicas: 2, minGpusPerWorker: 1, available: true },
  ],
}

// The Portal used to hard-code a fleet size, so a user could compose a job the
// server was always going to reject. Sizes now come from the deployment.
test('profiles are derived from the deployment ceilings rather than hard-coded', () => {
  const profiles = profilesFromLimits(limits)
  const rayTrain = profiles.find((profile) => profile.executionMode === 'ray_train')
  assert.equal(rayTrain.workers, 2)
  assert.equal(rayTrain.gpus, 8)
  assert.equal(rayTrain.workers * rayTrain.gpus <= limits.maxTotalGpus, true)
  assert.equal(profiles.every((profile) => profile.gpus <= limits.maxGpusPerWorker), true)
})

test('an unavailable profile is surfaced with its reason instead of being offered', () => {
  const singleNodeOnly = {
    ...limits,
    maxWorkerReplicas: 1,
    executionProfiles: limits.executionProfiles.map((profile) =>
      profile.mode === 'ray_train'
        ? { ...profile, available: false, unavailableReason: '当前集群单任务最多允许 1 个训练节点' }
        : profile,
    ),
  }
  const rayTrain = profilesFromLimits(singleNodeOnly).find((profile) => profile.executionMode === 'ray_train')
  assert.equal(rayTrain.available, false)
  assert.match(rayTrain.unavailableReason, /1 个训练节点/)
})

// This is the bug that made the form feel broken: picking the single-GPU card
// and then raising the GPU count left a stale mode, and the failure only
// appeared on submit. The mode is now derived from the numbers.
test('execution mode follows the selected worker and GPU counts', () => {
  assert.equal(resolveExecutionMode(1, 1), 'single_gpu')
  assert.equal(resolveExecutionMode(1, 8), 'torchrun')
  assert.equal(resolveExecutionMode(2, 8), 'ray_train')
  assert.equal(resolveExecutionMode(2, 1), 'ray_train')
})

test('resources are clamped to the deployment ceilings before submit', () => {
  assert.deepEqual(clampResources({ workers: 9, gpus: 99 }, limits), { workers: 2, gpus: 8, blocked: false })
  assert.deepEqual(clampResources({ workers: 0, gpus: 0 }, limits), { workers: 1, gpus: 1, blocked: false })
  // Total GPUs is a separate ceiling: two 8-GPU workers is exactly 16 here, but
  // a tighter total must reduce the worker count rather than silently pass.
  assert.deepEqual(clampResources({ workers: 2, gpus: 8 }, { ...limits, maxTotalGpus: 8 }), { workers: 1, gpus: 8, blocked: false })
})

test('zero remaining quota keeps the form minimum but explicitly blocks every profile', () => {
  const zeroQuotaLimits = {
    ...limits,
    maxWorkerReplicas: 0,
    maxGpusPerWorker: 0,
    maxTotalGpus: 0,
    tenantQuota: { gpuLimit: 8, gpuUsed: 8, gpuAvailable: 0 },
    executionProfiles: limits.executionProfiles.map((profile) => ({ ...profile, available: true })),
  }

  assert.deepEqual(clampResources({ workers: 3, gpus: 8 }, zeroQuotaLimits), { workers: 1, gpus: 1, blocked: true })
  const profiles = profilesFromLimits(zeroQuotaLimits)
  assert.equal(profiles.length > 0, true)
  assert.equal(profiles.every((profile) => profile.available === false), true)
  assert.equal(profiles.every((profile) => /没有可用 GPU/.test(profile.unavailableReason)), true)
})

test('zero physical capacity uses fleet copy for SuperAdmin profiles', () => {
  const profiles = profilesFromLimits({
    ...limits,
    maxWorkerReplicas: 0,
    maxGpusPerWorker: 0,
    maxTotalGpus: 0,
  })

  assert.equal(profiles.every((profile) => profile.available === false), true)
  assert.equal(profiles.every((profile) => /物理集群当前没有可用 GPU/.test(profile.unavailableReason)), true)
})

test('job quota model labels tenant limits as team availability and follows newly fetched capacity', () => {
  const tenantModel = jobQuotaModel({
    ...limits,
    maxWorkerReplicas: 5,
    maxGpusPerWorker: 10,
    maxTotalGpus: 37,
    tenantQuota: { gpuLimit: 40, gpuUsed: 3, gpuAvailable: 39 },
  })

  assert.deepEqual(tenantModel, {
    isTenantScoped: true,
    scopeLabel: '团队当前可用',
    maxWorkerReplicas: 5,
    maxGpusPerWorker: 10,
    maxTotalGpus: 37,
    gpuLimit: 40,
    gpuUsed: 3,
    gpuAvailable: 37,
    blocked: false,
    blockMessage: '',
  })
  assert.equal(JSON.stringify(tenantModel).includes('16'), false)
  assert.equal(JSON.stringify(tenantModel).includes('24'), false)
})

test('admin quota model separates physical fleet copy from own-team copy', () => {
  const superAdmin = adminQuotaModel({
    isSuperAdmin: true,
    limits: { ...limits, maxWorkerReplicas: 4, maxGpusPerWorker: 8, maxTotalGpus: 32 },
    tenants: [{ gpuQuotaLimit: 12 }, { gpuQuotaLimit: 9 }],
  })
  assert.match(superAdmin?.pageSummary || '', /物理集群容量.*32/)
  assert.match(superAdmin?.panelSummary || '', /已向租户分配总计 21/)

  const tenantAdmin = adminQuotaModel({
    isSuperAdmin: false,
    limits: { ...limits, maxTotalGpus: 5, tenantQuota: { gpuLimit: 8, gpuUsed: 3, gpuAvailable: 7 } },
    tenants: [{ gpuQuotaLimit: 8, gpuQuotaUsed: 3 }],
  })
  assert.equal(tenantAdmin?.title, '本团队 GPU 配额')
  assert.match(tenantAdmin?.pageSummary || '', /本团队.*管理员分配额度 8.*已使用 3.*当前可提交上限 5/)
  assert.doesNotMatch(`${tenantAdmin?.pageSummary} ${tenantAdmin?.panelSummary}`, /有效配额|当前可用|物理集群|全平台|各租户|分配总计/)
})

test('defaults stay conservative when the platform has not answered yet', () => {
  assert.equal(defaultPlatformLimits.maxWorkerReplicas >= 1, true)
  assert.equal(defaultPlatformLimits.mountPaths.dataset, '/mnt/data/input')
  assert.equal(defaultPlatformLimits.executionProfiles.length >= 2, true)
  assert.deepEqual(defaultPlatformLimits.cache, {
    enabled: false,
    defaultMode: 'off',
    modes: ['off'],
    allowedSizes: [],
    defaultSize: '',
    maxSize: '',
    mountPath: '',
  })
})

test('cache policy normalization is disabled and off-safe without a server policy', () => {
  assert.deepEqual(normalizeCachePolicy(), defaultPlatformLimits.cache)
  assert.deepEqual(
    normalizeCacheSelection({ cacheMode: 'runtime', cacheSize: '200Gi' }, normalizeCachePolicy()),
    { cacheMode: 'off', cacheSize: '' },
  )
})

test('selecting runtime cache uses the valid server default or first allowed size', () => {
  const policy = normalizeCachePolicy({
    enabled: true,
    defaultMode: 'off',
    modes: ['off', 'runtime'],
    allowedSizes: ['100Gi', '200Gi'],
    defaultSize: '200Gi',
    maxSize: '200Gi',
    mountPath: '/mnt/cache',
  })

  assert.deepEqual(
    normalizeCacheSelection({ cacheMode: 'runtime', cacheSize: '' }, policy, { selectRuntimeDefault: true }),
    { cacheMode: 'runtime', cacheSize: '200Gi' },
  )
  assert.deepEqual(
    normalizeCacheSelection(
      { cacheMode: 'runtime', cacheSize: '' },
      { ...policy, defaultSize: '500Gi' },
      { selectRuntimeDefault: true },
    ),
    { cacheMode: 'runtime', cacheSize: '100Gi' },
  )
})

test('cache selection follows a changed allowlist and switching off clears size', () => {
  const changedPolicy = normalizeCachePolicy({
    enabled: true,
    defaultMode: 'off',
    modes: ['off', 'runtime'],
    allowedSizes: ['50Gi', '100Gi'],
    defaultSize: '100Gi',
    maxSize: '100Gi',
    mountPath: '/mnt/cache',
  })

  assert.deepEqual(
    normalizeCacheSelection(
      { cacheMode: 'runtime', cacheSize: '200Gi' },
      changedPolicy,
      { selectRuntimeDefault: true },
    ),
    { cacheMode: 'runtime', cacheSize: '100Gi' },
  )
  assert.deepEqual(
    normalizeCacheSelection({ cacheMode: 'off', cacheSize: '100Gi' }, changedPolicy),
    { cacheMode: 'off', cacheSize: '' },
  )
  assert.deepEqual(
    normalizeCacheSelection(
      { cacheMode: 'runtime', cacheSize: '100Gi' },
      { ...changedPolicy, enabled: false },
      { selectRuntimeDefault: true },
    ),
    { cacheMode: 'off', cacheSize: '' },
  )
})

test('invalid copied cache query values stay off instead of adopting a different size', () => {
  const policy = normalizeCachePolicy({
    enabled: true,
    defaultMode: 'off',
    modes: ['off', 'runtime'],
    allowedSizes: ['100Gi'],
    defaultSize: '100Gi',
    maxSize: '100Gi',
    mountPath: '/mnt/cache',
  })

  assert.deepEqual(
    normalizeCacheSelection({ cacheMode: 'runtime', cacheSize: '200Gi' }, policy),
    { cacheMode: 'off', cacheSize: '' },
  )
})

test('legacy rerun and resume omit cache query values and start off after policy load', () => {
  const policy = normalizeCachePolicy({
    enabled: true,
    defaultMode: 'off',
    modes: ['off', 'runtime'],
    allowedSizes: ['100Gi', '200Gi'],
    defaultSize: '200Gi',
    maxSize: '200Gi',
    mountPath: '/mnt/cache',
  })
  const copiedQuery = cacheQueryForJob({ spec: {} })

  assert.deepEqual(copiedQuery, {})
  assert.deepEqual(normalizeCacheSelection(copiedQuery, policy), { cacheMode: 'off', cacheSize: '' })
})

test('runtime rerun and resume preserve only explicitly forwarded cache valid under current policy', () => {
  const copiedQuery = cacheQueryForJob({
    spec: { cache: { mode: 'runtime', size: '200Gi' } },
  })
  const currentPolicy = normalizeCachePolicy({
    enabled: true,
    defaultMode: 'off',
    modes: ['off', 'runtime'],
    allowedSizes: ['100Gi', '200Gi'],
    defaultSize: '100Gi',
    maxSize: '200Gi',
    mountPath: '/mnt/cache',
  })

  assert.deepEqual(copiedQuery, { cacheMode: 'runtime', cacheSize: '200Gi' })
  assert.deepEqual(normalizeCacheSelection(copiedQuery, currentPolicy), copiedQuery)
  assert.deepEqual(
    normalizeCacheSelection(copiedQuery, { ...currentPolicy, allowedSizes: ['100Gi'] }),
    { cacheMode: 'off', cacheSize: '' },
  )
})

test('runtime rerun carries automatic preload intent without exposing storage coordinates', () => {
  const copied = cacheQueryForJob({
    spec: { cache: { mode: 'runtime', size: '1Ti', preload: 'input' } },
  })
  assert.deepEqual(copied, { cacheMode: 'runtime', cacheSize: '1Ti', cachePreload: 'input' })
  assert.equal('storageClass' in copied, false)
  assert.equal('mountPath' in copied, false)
})
