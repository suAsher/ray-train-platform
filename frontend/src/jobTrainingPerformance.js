export const TRAINING_PERFORMANCE_WINDOWS = Object.freeze([
  Object.freeze({ value: '15m', label: '15 分钟' }),
  Object.freeze({ value: '1h', label: '1 小时' }),
  Object.freeze({ value: '6h', label: '6 小时' }),
  Object.freeze({ value: '24h', label: '24 小时' }),
])

export const TRAINING_PERFORMANCE_METRICS = Object.freeze([
  'cpuCores',
  'memoryWorkingSetBytes',
  'networkReceiveBytesPerSecond',
  'networkTransmitBytesPerSecond',
  'nodeNetworkReceiveBytesPerSecond',
  'nodeNetworkTransmitBytesPerSecond',
  'gpuUtilizationPercent',
  'gpuMemoryUsedMiB',
  'gpuPowerWatts',
  'gpuTemperatureCelsius',
  'objectStoreBytes',
  'objectStoreSpillBytesPerSecond',
  'cacheBytes',
  'cacheHitsTotal',
  'cacheMissesTotal',
  'cachePreloaderDurationSeconds',
  'stepTimeSeconds',
  'dataTimeSeconds',
  'ncclDurationSeconds',
  'step',
  'restarts',
  'state',
])

export function jobTrainingPerformancePath(jobId, window = '1h') {
  const selectedWindow = TRAINING_PERFORMANCE_WINDOWS.some(({ value }) => value === window) ? window : '1h'
  return `/api/v1/jobs/${encodeURIComponent(String(jobId ?? ''))}/training-performance?window=${encodeURIComponent(selectedWindow)}`
}

const objectOrEmpty = (value) => value && typeof value === 'object' && !Array.isArray(value) ? value : {}
const nullableText = (value) => typeof value === 'string' && value.trim() ? value.trim() : null

function nullableNumber(value) {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (typeof value !== 'string' || !value.trim()) return null
  const converted = Number(value)
  return Number.isFinite(converted) ? converted : null
}

function nullableInteger(value) {
  const converted = nullableNumber(value)
  return converted === null ? null : Math.trunc(converted)
}

function normalizePoint(point) {
  const source = objectOrEmpty(point)
  return {
    timestamp: nullableText(source.timestamp),
    value: nullableNumber(source.value),
  }
}

function normalizePoints(points) {
  return Array.isArray(points) ? points.map(normalizePoint) : []
}

function normalizeSummary(summary) {
  const source = objectOrEmpty(summary)
  const keys = [...new Set([...TRAINING_PERFORMANCE_METRICS, ...Object.keys(source)])]
  return Object.fromEntries(keys.map((key) => [key, nullableNumber(source[key])]))
}

function normalizeWorkerSeries(series) {
  const source = objectOrEmpty(series)
  const keys = [...new Set([...TRAINING_PERFORMANCE_METRICS, ...Object.keys(source)])]
  return Object.fromEntries(keys.map((key) => [key, normalizePoints(source[key])]))
}

function normalizeGlobalSeries(series) {
  const source = objectOrEmpty(series)
  const keys = [...new Set([...TRAINING_PERFORMANCE_METRICS, ...Object.keys(source)])]
  return Object.fromEntries(keys.map((key) => [
    key,
    Array.isArray(source[key])
      ? source[key].map((metricSeries) => {
          const item = objectOrEmpty(metricSeries)
          const labels = objectOrEmpty(item.labels)
          return {
            labels: Object.fromEntries(Object.entries(labels).map(([label, value]) => [label, nullableText(value)])),
            points: normalizePoints(item.points),
          }
        })
      : [],
  ]))
}

function normalizeWorker(worker) {
  const source = objectOrEmpty(worker)
  return {
    rank: nullableInteger(source.rank),
    pod: nullableText(source.pod),
    node: nullableText(source.node),
    gpu: nullableText(source.gpu),
    state: nullableText(source.state),
    restarts: nullableInteger(source.restarts),
    step: nullableInteger(source.step),
    series: normalizeWorkerSeries(source.series),
    summary: normalizeSummary(source.summary),
  }
}

function normalizeRecoveryPoint(point) {
  const source = objectOrEmpty(point)
  return {
    at: nullableText(source.at),
    clusterAttempt: nullableInteger(source.clusterAttempt),
    restartCount: nullableInteger(source.restartCount),
    resumeCheckpointId: nullableText(source.resumeCheckpointId),
    checkpointStep: nullableInteger(source.checkpointStep),
  }
}

export function normalizeTrainingPerformance(payload) {
  const source = objectOrEmpty(payload)
  const workload = objectOrEmpty(source.workload)
  return {
    workload: {
      namespace: nullableText(workload.namespace),
      rayClusterName: nullableText(workload.rayClusterName),
      rayJobName: nullableText(workload.rayJobName),
    },
    window: nullableText(source.window),
    stepSeconds: nullableInteger(source.stepSeconds),
    startedAt: nullableText(source.startedAt),
    endedAt: nullableText(source.endedAt),
    workers: Array.isArray(source.workers) ? source.workers.map(normalizeWorker) : [],
    series: normalizeGlobalSeries(source.series),
    summary: normalizeSummary(source.summary),
    unavailableMetrics: Array.isArray(source.unavailableMetrics)
      ? [...new Set(source.unavailableMetrics
          .filter((metric) => typeof metric === 'string' && TRAINING_PERFORMANCE_METRICS.includes(metric)))]
          .slice(0, TRAINING_PERFORMANCE_METRICS.length)
      : [],
    recovery: Array.isArray(source.recovery) ? source.recovery.map(normalizeRecoveryPoint) : [],
  }
}

function safeRatio(numerator, denominator) {
  const top = nullableNumber(numerator)
  const bottom = nullableNumber(denominator)
  if (top === null || bottom === null || bottom <= 0) return null
  const ratio = top / bottom
  return Number.isFinite(ratio) ? ratio : null
}

function diagnosis(code, severity, title, advice, ratios) {
  return { code, severity, title, advice, ratios: { ...ratios } }
}

export function diagnosePerformance(summary) {
  const metrics = objectOrEmpty(summary)
  const dataRatio = safeRatio(metrics.dataTimeSeconds, metrics.stepTimeSeconds)
  const ncclRatio = safeRatio(metrics.ncclDurationSeconds, metrics.stepTimeSeconds)
  const gpuUtilization = nullableNumber(metrics.gpuUtilizationPercent)
  const ratios = { data: dataRatio, nccl: ncclRatio }

  if (dataRatio !== null && dataRatio >= 0.2) {
    return diagnosis(
      'DATA_BOUND',
      'warning',
      '训练受数据供给限制',
      '建议检查 DataLoader 并发、输入缓存命中率与存储读取吞吐。',
      ratios,
    )
  }
  if (ncclRatio !== null && ncclRatio >= 0.2) {
    return diagnosis(
      'COMMUNICATION_BOUND',
      'warning',
      '训练受通信等待限制',
      '建议检查 rank 间负载是否均衡、NCCL 拓扑与节点网络吞吐。',
      ratios,
    )
  }
  if (gpuUtilization !== null && gpuUtilization < 50) {
    return diagnosis(
      'GPU_UNDERUTILIZED',
      'info',
      'GPU 利用率偏低',
      '建议结合 Worker 明细检查 CPU 准备、数据等待和训练批次大小。',
      ratios,
    )
  }
  return diagnosis(
    'BALANCED',
    'success',
    '训练性能较均衡',
    '当前未发现明显的数据、通信或 GPU 利用率瓶颈，可继续观察趋势。',
    ratios,
  )
}
