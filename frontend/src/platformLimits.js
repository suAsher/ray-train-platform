/**
 * Deployment ceilings and governed mount paths.
 *
 * The Portal previously hard-coded a fleet size, which let a user compose a job
 * the server was always going to reject. Everything here is either served by
 * `GET /api/v1/limits` or derived from it.
 */

export const defaultPlatformLimits = Object.freeze({
  maxWorkerReplicas: 1,
  maxGpusPerWorker: 1,
  maxTotalGpus: 1,
  mountPaths: Object.freeze({
    workspace: '/workspace',
    dataset: '/mnt/data/input',
    checkpoint: '/mnt/data/checkpoints',
    output: '/mnt/data/output',
  }),
  executionProfiles: Object.freeze([
    Object.freeze({ mode: 'single_gpu', name: '单卡', minWorkerReplicas: 1, maxWorkerReplicas: 1, minGpusPerWorker: 1, available: true }),
    Object.freeze({ mode: 'torchrun', name: '单机多卡 DDP', minWorkerReplicas: 1, maxWorkerReplicas: 1, minGpusPerWorker: 2, available: false }),
  ]),
  cache: Object.freeze({
    enabled: false,
    defaultMode: 'off',
    modes: Object.freeze(['off']),
    allowedSizes: Object.freeze([]),
    defaultSize: '',
    maxSize: '',
    mountPath: '',
    mountPaths: Object.freeze([]),
  }),
  datasets: Object.freeze({
    versioningEnabled: false,
    streamingEnabled: false,
    publisherEnabled: false,
  }),
})

export const zeroQuotaMessage = '团队当前没有可用 GPU 配额，请等待正在运行的任务释放资源，或联系超级管理员调整配额'

/** Normalize the cache descriptor served by GET /api/v1/limits. */
export function normalizeCachePolicy(policy) {
  if (policy?.enabled !== true) return {
    ...defaultPlatformLimits.cache,
    modes: [...defaultPlatformLimits.cache.modes],
    allowedSizes: [],
    mountPaths: [],
  }
  return {
    enabled: true,
    defaultMode: String(policy.defaultMode || '').trim(),
    modes: normalizedStrings(policy.modes),
    allowedSizes: normalizedStrings(policy.allowedSizes),
    defaultSize: String(policy.defaultSize || '').trim(),
    maxSize: String(policy.maxSize || '').trim(),
    mountPath: String(policy.mountPath || '').trim(),
    mountPaths: normalizedStrings(policy.mountPaths),
  }
}

/** Keep form and copied-query cache values inside the currently loaded policy. */
export function normalizeCacheSelection(selection, policy, { selectRuntimeDefault = false } = {}) {
  const cachePolicy = normalizeCachePolicy(policy)
  const mode = String(selection?.cacheMode || 'off').trim() || 'off'
  const size = String(selection?.cacheSize || '').trim()
  const runtimeAvailable = cachePolicy.enabled && cachePolicy.modes.includes('runtime') && cachePolicy.allowedSizes.length > 0
  if (mode !== 'runtime' || !runtimeAvailable) return { cacheMode: 'off', cacheSize: '' }
  if (cachePolicy.allowedSizes.includes(size)) return { cacheMode: 'runtime', cacheSize: size }
  if (!selectRuntimeDefault) return { cacheMode: 'off', cacheSize: '' }
  const defaultSize = cachePolicy.allowedSizes.includes(cachePolicy.defaultSize)
    ? cachePolicy.defaultSize
    : cachePolicy.allowedSizes[0]
  return defaultSize
    ? { cacheMode: 'runtime', cacheSize: defaultSize }
    : { cacheMode: 'off', cacheSize: '' }
}

/** Forward only an explicit, complete runtime cache request into copy/resubmit. */
export function cacheQueryForJob(job) {
  const mode = String(job?.spec?.cache?.mode || '').trim()
  const size = String(job?.spec?.cache?.size || '').trim()
	const preload = String(job?.spec?.cache?.preload || '').trim()
  const dataMode = String(job?.spec?.dataMode || '').trim()
  return mode === 'runtime' && size
	? { cacheMode: 'runtime', cacheSize: size, ...(preload === 'input' ? { cachePreload: 'input' } : {}), ...(dataMode ? { dataMode } : {}) }
    : {}
}

function normalizedStrings(values) {
  if (!Array.isArray(values)) return []
  return [...new Set(values.map((value) => String(value || '').trim()).filter(Boolean))]
}

const profileCopy = {
  single_gpu: {
    description: '先验证代码、数据与日志是否正确；命令在一张 GPU 上执行。',
    cpu: 8,
    memory: '32Gi',
  },
  torchrun: {
    description: '一台机器上的多张 GPU，由平台执行 torchrun。启动命令里不要自己写 torchrun。',
    cpu: 32,
    memory: '128Gi',
  },
  ray_train: {
    description: '多台机器，每台一个 worker，由 Ray 严格分散放置后在各节点启动 torchrun。',
    cpu: 32,
    memory: '128Gi',
  },
}

/**
 * Turn the server's execution profiles into the cards the submit form renders.
 * Each card is sized to the largest shape the deployment will actually admit.
 */
export function profilesFromLimits(limits) {
  const source = limits?.executionProfiles?.length ? limits.executionProfiles : defaultPlatformLimits.executionProfiles
  const blockMessage = jobQuotaModel(limits).blockMessage
  return source.map((profile) => {
    const copy = profileCopy[profile.mode] || { description: '', cpu: 8, memory: '32Gi' }
    const { workers, gpus, blocked } = largestShapeFor(profile, limits)
    return {
      executionMode: profile.mode,
      name: profile.name,
      description: copy.description,
      workers,
      gpus,
      cpu: copy.cpu,
      memory: copy.memory,
      available: profile.available !== false && !blocked,
      unavailableReason: blocked ? blockMessage : profile.unavailableReason || '',
    }
  })
}

function largestShapeFor(profile, limits) {
  const ceilings = { ...defaultPlatformLimits, ...(limits || {}) }
  const maxWorkers = Math.max(1, Math.min(profile.maxWorkerReplicas || ceilings.maxWorkerReplicas, ceilings.maxWorkerReplicas))
  const workers = Math.max(profile.minWorkerReplicas || 1, Math.min(maxWorkers, ceilings.maxWorkerReplicas))
  const gpuCeiling = Math.min(ceilings.maxGpusPerWorker, Math.floor(ceilings.maxTotalGpus / Math.max(1, workers)))
  const gpus = Math.max(profile.minGpusPerWorker || 1, Math.max(1, gpuCeiling))
  return clampResources({ workers, gpus }, ceilings)
}

/**
 * Derive the execution contract from the numbers the user picked.
 *
 * Keeping the mode as separate state is what made the form feel broken:
 * choosing the single-GPU card and then raising the GPU count left a stale
 * mode, and the mismatch only surfaced as a rejected submit.
 */
export function resolveExecutionMode(workers, gpus) {
  const workerCount = Math.max(1, Number(workers) || 1)
  const gpuCount = Math.max(1, Number(gpus) || 1)
  if (workerCount > 1) return 'ray_train'
  return gpuCount > 1 ? 'torchrun' : 'single_gpu'
}

/** Clamp a requested shape to what the deployment admits. */
export function clampResources({ workers, gpus }, limits) {
  const ceilings = { ...defaultPlatformLimits, ...(limits || {}) }
  const blocked = [ceilings.maxWorkerReplicas, ceilings.maxGpusPerWorker, ceilings.maxTotalGpus]
    .some((value) => nonNegativeInteger(value) === 0)
  if (blocked) return { workers: 1, gpus: 1, blocked: true }
  const boundedGpus = Math.max(1, Math.min(Number(gpus) || 1, ceilings.maxGpusPerWorker, ceilings.maxTotalGpus))
  const workerCeiling = Math.min(ceilings.maxWorkerReplicas, Math.floor(ceilings.maxTotalGpus / boundedGpus))
  const boundedWorkers = Math.max(1, Math.min(Number(workers) || 1, Math.max(1, workerCeiling)))
  return { workers: boundedWorkers, gpus: boundedGpus, blocked: false }
}

/** Copy and values shown beside the submit form. */
export function jobQuotaModel(limits = {}) {
  const tenantQuota = limits?.tenantQuota
  const isTenantScoped = tenantQuota != null
  const maxWorkerReplicas = nonNegativeInteger(limits.maxWorkerReplicas)
  const maxGpusPerWorker = nonNegativeInteger(limits.maxGpusPerWorker)
  const maxTotalGpus = nonNegativeInteger(limits.maxTotalGpus)
  const gpuLimit = isTenantScoped ? nonNegativeInteger(tenantQuota.gpuLimit) : maxTotalGpus
  const gpuUsed = isTenantScoped ? nonNegativeInteger(tenantQuota.gpuUsed) : 0
  // maxTotalGpus is the backend's physical-and-quota-clamped ceiling for a new
  // job. gpuLimit remains visible as the administrator-assigned team budget.
  const gpuAvailable = maxTotalGpus
  const blocked = maxWorkerReplicas === 0 || maxGpusPerWorker === 0 || maxTotalGpus === 0 || gpuAvailable === 0
  return {
    isTenantScoped,
    scopeLabel: isTenantScoped ? '团队当前可用' : '物理集群容量',
    maxWorkerReplicas,
    maxGpusPerWorker,
    maxTotalGpus,
    gpuLimit,
    gpuUsed,
    gpuAvailable,
    blocked,
    blockMessage: blocked
      ? (isTenantScoped ? zeroQuotaMessage : '物理集群当前没有可用 GPU，请等待资源恢复后再提交')
      : '',
  }
}

/** Role-aware quota-console copy. TenantAdmin responses contain only their own tenant. */
export function adminQuotaModel({ isSuperAdmin = false, limits = {}, tenants = [] } = {}) {
  const jobModel = jobQuotaModel(limits)
  const allocatedGPUs = tenants.reduce((total, tenant) => total + nonNegativeInteger(tenant?.gpuQuotaLimit), 0)
  if (isSuperAdmin) {
    return {
      title: '租户 GPU 配额',
      pageSummary: `物理集群容量 ${jobModel.maxTotalGpus} 张 GPU（单任务最多 ${jobModel.maxWorkerReplicas} 节点 × ${jobModel.maxGpusPerWorker} 卡）；当前已向租户分配总计 ${allocatedGPUs} 张。`,
      panelSummary: `物理集群 GPU 容量 ${jobModel.maxTotalGpus} 卡，已向租户分配总计 ${allocatedGPUs} 卡。`,
      capacityGPUs: jobModel.maxTotalGpus,
      allocatedGPUs,
    }
  }

  const ownTenant = tenants[0] || {}
  const gpuLimit = jobModel.isTenantScoped ? jobModel.gpuLimit : nonNegativeInteger(ownTenant.gpuQuotaLimit)
  const gpuUsed = jobModel.isTenantScoped ? jobModel.gpuUsed : nonNegativeInteger(ownTenant.gpuQuotaUsed)
  const summary = `当前仅显示本团队：管理员分配额度 ${gpuLimit} 卡，已使用 ${gpuUsed} 卡，当前可提交上限 ${jobModel.maxTotalGpus} 卡。`
  return {
    title: '本团队 GPU 配额',
    pageSummary: summary,
    panelSummary: summary,
    capacityGPUs: gpuLimit,
    allocatedGPUs: gpuLimit,
  }
}

function nonNegativeInteger(value) {
  return Math.max(0, Math.floor(Number(value) || 0))
}

/** The path a chosen logical directory appears at inside the container. */
export function containerPathFor(role, limits) {
  const paths = { ...defaultPlatformLimits.mountPaths, ...(limits?.mountPaths || {}) }
  return paths[role] || ''
}
