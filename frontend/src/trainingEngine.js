export const RAY_DDP_ENGINE = 'ray-ddp'
export const RAY_TRAIN_ENGINE = 'ray-train'

/** Legacy rows have no trainingEngine and must remain Ray-orchestrated DDP. */
export function normalizeTrainingEngine(value) {
  return String(value || '').trim() === RAY_TRAIN_ENGINE ? RAY_TRAIN_ENGINE : RAY_DDP_ENGINE
}

export function trainingEngineLabel(value) {
  const requested = value && typeof value === 'object' ? value.trainingEngine : value
  return normalizeTrainingEngine(requested) === RAY_TRAIN_ENGINE ? 'Ray Train 托管' : 'Ray 编排 DDP'
}

export function managedEngineAvailability({ limits = {}, images = [], imageReference = '' } = {}) {
  const runtime = limits.runtime || {}
  const engines = Array.isArray(runtime.availableEngines) ? runtime.availableEngines : []
  if (runtime.managedEnabled !== true || !engines.includes(RAY_TRAIN_ENGINE)) {
    return {
      available: false,
      reason: String(runtime.managedUnavailableReason || '当前团队未开放 Ray Train 托管').trim(),
    }
  }

  const selectedReference = String(imageReference || '').trim()
  if (!selectedReference) return { available: false, reason: '请先选择训练镜像' }
  const image = images.find((candidate) => String(candidate?.reference || '').trim() === selectedReference)
  if (!image) return { available: false, reason: '所选镜像缺少平台兼容性信息' }
  const supported = Array.isArray(image.supportedEngines) ? image.supportedEngines : []
  if (!supported.includes(RAY_TRAIN_ENGINE)) {
    return { available: false, reason: '所选镜像不支持 Ray Train 托管，请选择兼容镜像' }
  }
  return { available: true, reason: '' }
}

export function resubmitRuntimeQuery(job, { resume = false } = {}) {
  const spec = job?.spec || {}
  const managed = spec.managed || {}
  const checkpoint = managed.checkpoint || {}
  const query = {
    trainingEngine: normalizeTrainingEngine(spec.trainingEngine),
    maxFailures: String(nonNegativeInteger(managed.maxFailures, 2)),
    checkpointEveryEpochs: String(nonNegativeInteger(checkpoint.everyEpochs, 1)),
    checkpointKeepLatest: String(nonNegativeInteger(checkpoint.keepLatest, 3)),
    checkpointKeepBest: String(nonNegativeInteger(checkpoint.keepBest, 1)),
  }
  return resume && job?.id ? { ...query, parentJobId: String(job.id) } : query
}

export function jobRuntimeDetails(job) {
  const spec = job?.spec || {}
  const resources = spec.resources || {}
  const workerCount = nonNegativeInteger(resources.workerReplicas, 0)
  const gpusPerWorker = nonNegativeInteger(resources.gpusPerWorker, 0)
  const engine = normalizeTrainingEngine(spec.trainingEngine)
  return {
    engine,
    engineLabel: trainingEngineLabel(engine),
    rayVersion: String(spec.rayVersion || '').trim() || '—',
    image: String(spec.imageDigest || spec.image || '').trim() || '—',
    workerCount,
    worldSize: workerCount * gpusPerWorker,
    clusterAttempt: nonNegativeInteger(job?.clusterAttempt, 0),
    restartCount: nonNegativeInteger(job?.workerRestartCount, 0),
    resumeSource: String(job?.resumeCheckpointId || spec.parentJobId || '').trim(),
  }
}

function nonNegativeInteger(value, fallback) {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : fallback
}
