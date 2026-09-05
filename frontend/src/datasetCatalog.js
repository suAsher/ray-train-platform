const digestPattern = /^[0-9a-f]{64}$/
const identifierPattern = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/
const readyState = 'READY'

const text = (value) => typeof value === 'string' ? value.trim() : ''

export function normalizeDatasetSites(value) {
  if (value === undefined) return []
  if (!Array.isArray(value) || value.length > 256 || value.some((site) => typeof site !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(site))) {
    throw new Error('场地编码需为字母、数字、下划线或连字符，最多选择 256 个场地')
  }
  return [...new Set(value)].sort()
}
const count = (value) => {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : 0
}

export function normalizeDatasetCapabilities(value) {
  const source = value && typeof value === 'object' && !Array.isArray(value) ? value : {}
  const versioningEnabled = source.versioningEnabled === true
  return {
    versioningEnabled,
    streamingEnabled: versioningEnabled && source.streamingEnabled === true,
    publisherEnabled: versioningEnabled && source.publisherEnabled === true,
    catalogEnabled: versioningEnabled,
  }
}

export function streamingDatasetAvailability({ limits = {}, images = [], imageReference = '' } = {}) {
  const capabilities = normalizeDatasetCapabilities(limits.datasets)
  if (!capabilities.streamingEnabled) {
    return { available: false, reason: '当前团队未开放版本化 Ray Data 流式训练', rayVersion: '' }
  }
  const runtime = limits.runtime && typeof limits.runtime === 'object' ? limits.runtime : {}
  const engines = Array.isArray(runtime.availableEngines) ? runtime.availableEngines : []
  if (runtime.managedEnabled !== true || runtime.canaryEnabled !== true || !engines.includes('ray-train')) {
    return { available: false, reason: '当前团队未开放 Ray Train 2.58 canary', rayVersion: '' }
  }
  const canaryRayVersion = text(runtime.canaryRayVersion) || '2.58.0'
  const reference = text(imageReference)
  const image = Array.isArray(images)
    ? images.find((candidate) => text(candidate?.reference) === reference)
    : null
  if (!image) return { available: false, reason: '请先选择带版本信息的训练镜像', rayVersion: canaryRayVersion }
  if (!Array.isArray(image.supportedEngines) || !image.supportedEngines.includes('ray-train')) {
    return { available: false, reason: '所选镜像不支持 Ray Train 托管', rayVersion: canaryRayVersion }
  }
  if (text(image.rayVersion) !== canaryRayVersion) {
    return { available: false, reason: `版本化流式训练需要 Ray ${canaryRayVersion} 兼容镜像`, rayVersion: canaryRayVersion }
  }
  return { available: true, reason: '', rayVersion: canaryRayVersion }
}

export function normalizeDatasetList(values) {
  if (!Array.isArray(values)) return []
  return values.map((value) => ({
    id: text(value?.id),
    slug: text(value?.slug),
    name: text(value?.name),
    description: text(value?.description),
    sourceSpace: text(value?.sourceSpace),
    sourceRelativePath: text(value?.sourceRelativePath),
    ownerTenantId: text(value?.ownerTenantId),
    visibility: text(value?.visibility),
    schemaVersion: text(value?.schemaVersion),
  })).filter(({ id, slug }) => id && slug)
}

export function normalizeDatasetVersions(values) {
  if (!Array.isArray(values)) return []
  return values.map((value) => ({
    id: text(value?.id),
    datasetId: text(value?.datasetId),
    version: text(value?.version),
    state: text(value?.state).toUpperCase(),
    manifestSha256: text(value?.manifestSha256),
    schemaVersion: text(value?.schemaVersion),
    trainSamples: count(value?.trainSamples),
    valSamples: count(value?.valSamples),
    testSamples: count(value?.testSamples),
    sourceObjectCount: count(value?.sourceObjectCount),
    logicalBytes: count(value?.logicalBytes),
    packedBytes: count(value?.packedBytes),
  })).filter(({ id, datasetId, version }) => id && datasetId && version)
}

export function datasetVersionOptions(values) {
  const ready = normalizeDatasetVersions(values).filter((version) => version.state === readyState)
  if (!ready.length) return []
  const latest = ready[0]
  return [
    {
      value: 'latest',
      resolvedVersionId: latest.id,
      label: `最新可用（${latest.version}）`,
      version: latest,
    },
    ...ready.map((version) => ({
      value: version.id,
      resolvedVersionId: version.id,
      label: version.version,
      version,
    })),
  ]
}

export function datasetVersionDelta(currentValue, previousValue) {
  const current = normalizeDatasetVersions([{ id: 'current', datasetId: 'dataset', version: 'current', ...currentValue }])[0]
  const previous = normalizeDatasetVersions([{ id: 'previous', datasetId: 'dataset', version: 'previous', ...previousValue }])[0]
  const fields = ['trainSamples', 'valSamples', 'testSamples', 'sourceObjectCount', 'logicalBytes', 'packedBytes']
  return Object.fromEntries(fields.map((field) => [field, (current?.[field] || 0) - (previous?.[field] || 0)]))
}

export function datasetVersionPresentation(state) {
  return {
    DISCOVERING: { label: '发现数据', type: 'info' },
    STABILIZING: { label: '等待稳定', type: 'warning' },
    VALIDATING: { label: '校验中', type: 'warning' },
    PACKING: { label: '打包中', type: 'warning' },
    READY: { label: '可训练', type: 'success' },
    FAILED: { label: '发布失败', type: 'danger' },
    DEPRECATED: { label: '已弃用', type: 'info' },
    RETIRED: { label: '已回收', type: 'info' },
  }[text(state).toUpperCase()] || { label: '状态未知', type: 'info' }
}

export function formatDatasetBytes(value) {
  if (value == null || value === '') return '—'
  const bytes = Number(value)
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const scaled = bytes / 1024 ** index
  return `${scaled.toLocaleString('zh-CN', { maximumFractionDigits: 2 })} ${units[index]}`
}

export function formatDatasetCount(value) {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= 0 ? Math.trunc(parsed).toLocaleString('zh-CN') : '—'
}

export function preflightFingerprint(spec) {
  if (!spec || spec.dataMode !== 'streaming') return ''
  // A successful check belongs to the exact request the server evaluated.
  // Any later code, command, resource, output or recovery-policy edit must
  // hide the old result and force another side-effect-free preflight.
  return JSON.stringify(spec)
}

export function assertStreamingPreflightCurrent(requestedSpec, currentSpec) {
  const requested = preflightFingerprint(requestedSpec)
  const current = preflightFingerprint(currentSpec)
  if (!requested || requested !== current) {
    throw new Error('提交配置已在检查期间发生变化，请重新检查后提交')
  }
}

export function pinStreamingPreflight(spec, result, expectedRayVersion) {
  if (!spec || spec.dataMode !== 'streaming') throw new Error('提交前检查只适用于版本化流式训练')
  const dataset = result?.dataset
  const sites = normalizeDatasetSites(spec.datasetRef?.sites)
  if (JSON.stringify(sites) !== JSON.stringify(normalizeDatasetSites(dataset?.sites))) {
    throw new Error('提交前检查返回的场地范围与当前选择不一致')
  }
  const rayVersion = text(expectedRayVersion)
  const requestedGPUs = Number(spec.resources?.workerReplicas || 0) * Number(spec.resources?.gpusPerWorker || 0)
  const validDataset = dataset && text(dataset.datasetId) && text(dataset.versionId) && text(dataset.versionId) !== 'latest' &&
    digestPattern.test(text(dataset.manifestSha256)) && count(dataset.trainSamples) > 0 && count(dataset.packedBytes) > 0 &&
    text(dataset.dataMode) === 'streaming' && text(dataset.cachePolicy) === text(spec.cachePolicy)
  if (!validDataset) throw new Error('提交前检查返回了无效的数据版本')
  if (!identifierPattern.test(rayVersion) || text(result.image) !== text(spec.image) ||
      text(result.trainingEngine) !== 'ray-train' || text(result.rayVersion) !== rayVersion) {
    throw new Error('提交前检查返回的运行环境与当前选择不一致')
  }
  if (Number(result.requestedGpus) !== requestedGPUs) throw new Error('提交前检查返回的资源申请与当前选择不一致')
  return {
    ...spec,
    datasetRef: { dataset: text(dataset.datasetId), version: text(dataset.versionId), ...(sites.length ? { sites } : {}) },
  }
}
