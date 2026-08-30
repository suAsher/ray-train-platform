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
  'objectStoreSpillBytesTotal',
  'objectStoreSpillBytes',
  'cacheBytes',
  'cacheHitsTotal',
  'cacheMissesTotal',
  'cachePreloaderDurationSeconds',
  'stepTimeSeconds',
  'dataTimeSeconds',
  'ncclDurationSeconds',
  'step',
  'datasetSourceBytesTotal',
  'datasetSourceBytesPerSecond',
  'datasetSourceReadsPerSecond',
  'datasetSourceFilesPerSecond',
  'datasetSourceShardsPerSecond',
  'datasetSourceSamplesPerSecond',
  'datasetSourceReadP95Seconds',
  'datasetCacheReadP95Seconds',
  'datasetPrefetchWaitP95Seconds',
  'restarts',
  'state',
  'datasetPrefetchWaitSecondsTotal',
  'datasetPrefetchWaitRatio',
  'datasetBackpressureSecondsTotal',
  'datasetBackpressureRatio',
  'datasetSourceReadSecondsTotal',
  'datasetCacheReadSecondsTotal',
  'datasetBatchesTotal',
  'datasetSamplesTotal',
  'datasetShardReadsTotal',
  'datasetSourceReadsTotal',
  'datasetCacheReadsTotal',
  'datasetCacheBytesReadTotal',
  'datasetCacheBytesPerSecond',
  'datasetCacheHitsTotal',
  'datasetCacheMissesTotal',
  'datasetCacheHitRatio',
  'datasetCacheDownloadsTotal',
  'datasetCacheFallbacksTotal',
  'datasetCacheChecksumFailuresTotal',
  'datasetCacheEvictionsTotal',
  'datasetCacheStaleTempReclaimedTotal',
  'datasetCacheBytesTotal',
])

const SAFE_TRAINING_PERFORMANCE_LABELS = new Set([
  'pod',
  'exported_pod',
  'node',
  'rank',
  'worker_rank',
  'gpu',
  'UUID',
  'state',
  'phase',
  'dataset_id',
  'dataset_version_id',
  'ray_version',
  'data_mode',
  'cache_policy',
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
  return Object.fromEntries(TRAINING_PERFORMANCE_METRICS.map((key) => [key, nullableNumber(source[key])]))
}

function normalizeWorkerSeries(series) {
  const source = objectOrEmpty(series)
  return Object.fromEntries(TRAINING_PERFORMANCE_METRICS.map((key) => [key, normalizePoints(source[key])]))
}

function normalizeGlobalSeries(series) {
  const source = objectOrEmpty(series)
  return Object.fromEntries(TRAINING_PERFORMANCE_METRICS.map((key) => [
    key,
    Array.isArray(source[key])
      ? source[key].map((metricSeries) => {
          const item = objectOrEmpty(metricSeries)
          const labels = objectOrEmpty(item.labels)
          return {
            labels: Object.fromEntries(Object.entries(labels)
              .filter(([label]) => SAFE_TRAINING_PERFORMANCE_LABELS.has(label))
              .map(([label, value]) => [label, nullableText(value)])),
            points: normalizePoints(item.points),
          }
        })
      : [],
  ]))
}

function timestampMilliseconds(value) {
  const converted = typeof value === 'string' ? Date.parse(value) : Number.NaN
  return Number.isFinite(converted) ? converted : null
}

function validTimedPoints(points) {
  return normalizePoints(points)
    .map((point) => ({ ...point, milliseconds: timestampMilliseconds(point.timestamp) }))
    .filter((point) => point.milliseconds !== null && point.value !== null)
    .sort((left, right) => left.milliseconds - right.milliseconds)
}

function counterSeriesRate(metricSeries) {
  const points = validTimedPoints(metricSeries?.points)
  if (points.length < 2) return null
  const elapsedSeconds = (points.at(-1).milliseconds - points[0].milliseconds) / 1_000
  if (!Number.isFinite(elapsedSeconds) || elapsedSeconds <= 0) return null

  let increase = 0
  for (let index = 1; index < points.length; index += 1) {
    const previous = points[index - 1].value
    const current = points[index].value
    increase += current >= previous ? current - previous : Math.max(0, current)
  }
  const rate = increase / elapsedSeconds
  return Number.isFinite(rate) && rate >= 0 ? rate : null
}

function aggregateCounterRate(metricSeries, reducer = 'sum') {
  const rates = Array.isArray(metricSeries)
    ? metricSeries.map(counterSeriesRate).filter((value) => value !== null)
    : []
  if (!rates.length) return null
  const total = rates.reduce((sum, value) => sum + value, 0)
  return reducer === 'average' ? total / rates.length : total
}

function integrateRateSeries(metricSeries) {
  if (!Array.isArray(metricSeries)) return null
  let bytes = 0
  let intervals = 0
  for (const series of metricSeries) {
    const points = validTimedPoints(series?.points)
    for (let index = 1; index < points.length; index += 1) {
      const previous = points[index - 1]
      const current = points[index]
      const elapsedSeconds = (current.milliseconds - previous.milliseconds) / 1_000
      if (!Number.isFinite(elapsedSeconds) || elapsedSeconds <= 0) continue
      bytes += Math.max(0, (previous.value + current.value) / 2) * elapsedSeconds
      intervals += 1
    }
  }
  return intervals > 0 && Number.isFinite(bytes) ? bytes : null
}

function firstNumber(...values) {
  for (const value of values) {
    const converted = nullableNumber(value)
    if (converted !== null) return converted
  }
  return null
}

function finiteRatio(value) {
  const converted = nullableNumber(value)
  return converted !== null && converted >= 0 && converted <= 1 ? converted : null
}

function streamingSummary(summary, series) {
  const normalized = { ...summary }
  const sourceBytesPerSecond = aggregateCounterRate(series.datasetSourceBytesTotal)
  const sourceShardsPerSecond = aggregateCounterRate(series.datasetSourceReadsTotal)
  const cacheBytesPerSecond = aggregateCounterRate(series.datasetCacheBytesReadTotal)
  const sourceSamplesPerSecond = aggregateCounterRate(series.datasetSamplesTotal)
  const prefetchWaitRatio = aggregateCounterRate(series.datasetPrefetchWaitSecondsTotal, 'average')
  const backpressureRatio = aggregateCounterRate(series.datasetBackpressureSecondsTotal, 'average')
  const cacheHits = nullableNumber(summary.datasetCacheHitsTotal)
  const cacheMisses = nullableNumber(summary.datasetCacheMissesTotal)
  const cacheHitRatio = cacheHits === null || cacheMisses === null
    ? null
    : safeRatio(cacheHits, cacheHits + cacheMisses)

  return {
    ...normalized,
    datasetSourceBytesPerSecond: firstNumber(summary.datasetSourceBytesPerSecond, sourceBytesPerSecond),
    datasetSourceShardsPerSecond: firstNumber(
      summary.datasetSourceShardsPerSecond,
      summary.datasetSourceReadsPerSecond,
      sourceShardsPerSecond,
    ),
    datasetCacheBytesPerSecond: firstNumber(summary.datasetCacheBytesPerSecond, cacheBytesPerSecond),
    datasetSourceSamplesPerSecond: firstNumber(summary.datasetSourceSamplesPerSecond, sourceSamplesPerSecond),
    datasetSourceReadP95Seconds: firstNumber(summary.datasetSourceReadP95Seconds),
    datasetPrefetchWaitRatio: finiteRatio(firstNumber(summary.datasetPrefetchWaitRatio, prefetchWaitRatio)),
    datasetBackpressureRatio: finiteRatio(firstNumber(summary.datasetBackpressureRatio, backpressureRatio)),
    datasetCacheHitRatio: finiteRatio(firstNumber(summary.datasetCacheHitRatio, cacheHitRatio)),
    objectStoreSpillBytes: firstNumber(
      summary.objectStoreSpillBytes,
      summary.objectStoreSpillBytesTotal,
      integrateRateSeries(series.objectStoreSpillBytesPerSecond),
    ),
  }
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
  const series = normalizeGlobalSeries(source.series)
  const summary = streamingSummary(normalizeSummary(source.summary), series)
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
    series,
    summary,
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

function gpuDataStall(metrics, ratios) {
  const gpuUtilization = nullableNumber(metrics.gpuUtilizationPercent)
  const candidates = [
    ['data', ratios.data],
    ['prefetch', ratios.prefetch],
    ['backpressure', ratios.backpressure],
  ]
  const measured = candidates.filter(([, ratio]) => ratio !== null)
  if (gpuUtilization === null || measured.length === 0) {
    return {
      status: 'unknown',
      signal: null,
      reason: '缺少 GPU 利用率或数据等待指标，暂时无法判断。',
    }
  }
  const stalled = measured.find(([, ratio]) => ratio >= 0.2)
  if (gpuUtilization < 70 && stalled) {
    const descriptions = {
      data: '训练数据等待',
      prefetch: 'Ray 预取等待',
      backpressure: 'Ray 背压等待',
    }
    return {
      status: 'detected',
      signal: stalled[0],
      reason: `GPU 利用率偏低且${descriptions[stalled[0]]}达到 ${Math.round(stalled[1] * 100)}%。`,
    }
  }
  return {
    status: 'clear',
    signal: null,
    reason: '现有采样未显示 GPU 因数据供给发生明显停顿。',
  }
}

function diagnosis(code, severity, title, advice, ratios, dataStall) {
  return { code, severity, title, advice, ratios: { ...ratios }, dataStall: { ...dataStall } }
}

export function diagnosePerformance(summary) {
  const metrics = objectOrEmpty(summary)
  const dataRatio = safeRatio(metrics.dataTimeSeconds, metrics.stepTimeSeconds)
  const ncclRatio = safeRatio(metrics.ncclDurationSeconds, metrics.stepTimeSeconds)
  const prefetchRatio = finiteRatio(metrics.datasetPrefetchWaitRatio)
  const backpressureRatio = finiteRatio(metrics.datasetBackpressureRatio)
  const gpuUtilization = nullableNumber(metrics.gpuUtilizationPercent)
  const ratios = { data: dataRatio, nccl: ncclRatio, prefetch: prefetchRatio, backpressure: backpressureRatio }
  const dataStall = gpuDataStall(metrics, ratios)

  if (dataRatio !== null && dataRatio >= 0.2) {
    return diagnosis(
      'DATA_BOUND',
      'warning',
      '训练受数据供给限制',
      '建议检查 DataLoader 并发、输入缓存命中率与存储读取吞吐。',
      ratios,
      dataStall,
    )
  }
  if (ncclRatio !== null && ncclRatio >= 0.2) {
    return diagnosis(
      'COMMUNICATION_BOUND',
      'warning',
      '训练受通信等待限制',
      '建议检查 rank 间负载是否均衡、NCCL 拓扑与节点网络吞吐。',
      ratios,
      dataStall,
    )
  }
  if (gpuUtilization !== null && gpuUtilization < 50) {
    return diagnosis(
      'GPU_UNDERUTILIZED',
      'info',
      'GPU 利用率偏低',
      '建议结合 Worker 明细检查 CPU 准备、数据等待和训练批次大小。',
      ratios,
      dataStall,
    )
  }
  return diagnosis(
    'BALANCED',
    'success',
    '训练性能较均衡',
    '当前未发现明显的数据、通信或 GPU 利用率瓶颈，可继续观察趋势。',
    ratios,
    dataStall,
  )
}
