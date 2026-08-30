import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8')
const loadPerformanceModule = () => import('./jobTrainingPerformance.js')

test('builds the scoped training-performance path with only approved windows', async () => {
  const { jobTrainingPerformancePath, TRAINING_PERFORMANCE_WINDOWS } = await loadPerformanceModule()

  assert.deepEqual(TRAINING_PERFORMANCE_WINDOWS.map(({ value }) => value), ['15m', '1h', '6h', '24h'])
  assert.equal(
    jobTrainingPerformancePath('job /中文?#', '24h'),
    '/api/v1/jobs/job%20%2F%E4%B8%AD%E6%96%87%3F%23/training-performance?window=24h',
  )
  assert.equal(
    jobTrainingPerformancePath('job-a', '24h&pod=attacker'),
    '/api/v1/jobs/job-a/training-performance?window=1h',
  )
  assert.equal(
    jobTrainingPerformancePath('job-a'),
    '/api/v1/jobs/job-a/training-performance?window=1h',
  )
})

test('normalizes training performance without aliases or false zero values', async () => {
  const { normalizeTrainingPerformance } = await loadPerformanceModule()
  const payload = {
    workload: { namespace: 'tenant-a', rayClusterName: 'cluster-a', rayJobName: 'job-a' },
    window: '1h',
    stepSeconds: 30,
    startedAt: '2026-08-28T01:00:00Z',
    endedAt: '2026-08-28T02:00:00Z',
    workers: [{
      rank: 0,
      pod: 'cluster-a-worker-0',
      node: 'node-a',
      gpu: 'GPU-0',
      state: 'Running',
      restarts: 0,
      step: null,
      series: {
        gpuUtilizationPercent: [{ timestamp: '2026-08-28T01:59:30Z', value: 0 }],
      },
      summary: {
        gpuUtilizationPercent: 0,
        dataTimeSeconds: null,
        cpuCores: 2.5,
      },
    }],
    series: {
      stepTimeSeconds: [{
        labels: { pod: 'cluster-a-worker-0' },
        points: [{ timestamp: '2026-08-28T01:59:30Z', value: 4 }],
      }],
    },
    summary: {
      stepTimeSeconds: 4,
      dataTimeSeconds: null,
      ncclDurationSeconds: Number.NaN,
      gpuUtilizationPercent: 0,
      cacheHitsTotal: 8,
      cacheMissesTotal: 2,
    },
    unavailableMetrics: ['gpuPowerWatts', '', 'gpuPowerWatts', 9],
    recovery: [{
      at: '2026-08-28T01:30:00Z',
      clusterAttempt: 2,
      restartCount: 0,
      resumeCheckpointId: 'checkpoint-1',
      checkpointStep: null,
    }],
  }
  const original = structuredClone(payload)
  const normalized = normalizeTrainingPerformance(payload)

  assert.equal(normalized.workers[0].rank, 0)
  assert.equal(normalized.workers[0].restarts, 0)
  assert.equal(normalized.workers[0].step, null)
  assert.equal(normalized.workers[0].summary.gpuUtilizationPercent, 0)
  assert.equal(normalized.workers[0].summary.dataTimeSeconds, null)
  assert.equal(normalized.workers[0].summary.memoryWorkingSetBytes, null)
  assert.equal(normalized.summary.dataTimeSeconds, null)
  assert.equal(normalized.summary.ncclDurationSeconds, null)
  assert.equal(normalized.summary.objectStoreSpillBytesPerSecond, null)
  assert.equal(normalized.summary.cacheHitsTotal, 8)
  assert.equal(normalized.summary.cacheMissesTotal, 2)
  assert.equal(normalized.summary.cacheHitsPerSecond, undefined)
  assert.deepEqual(normalized.unavailableMetrics, ['gpuPowerWatts'])
  assert.equal(normalized.recovery[0].checkpointStep, null)

  normalized.workers[0].summary.cpuCores = 99
  normalized.workers[0].series.gpuUtilizationPercent[0].value = 99
  normalized.series.stepTimeSeconds[0].labels.pod = 'changed'
  assert.deepEqual(payload, original)
})

test('normalizer keeps nulls for an empty or malformed response', async () => {
  const { normalizeTrainingPerformance } = await loadPerformanceModule()
  const normalized = normalizeTrainingPerformance({
    stepSeconds: '',
    workers: [{ rank: '', restarts: undefined, step: 'not-a-number', summary: null }],
    summary: null,
  })

  assert.equal(normalized.window, null)
  assert.equal(normalized.stepSeconds, null)
  assert.equal(normalized.startedAt, null)
  assert.equal(normalized.workload.namespace, null)
  assert.equal(normalized.workers[0].rank, null)
  assert.equal(normalized.workers[0].restarts, null)
  assert.equal(normalized.workers[0].step, null)
  assert.equal(normalized.summary.gpuUtilizationPercent, null)
  assert.deepEqual(normalized.series.gpuUtilizationPercent, [])
  assert.deepEqual(normalized.recovery, [])
  assert.deepEqual(normalized.unavailableMetrics, [])
})

test('derives streaming source, Ray wait, bounded cache, and spill details from safe telemetry', async () => {
  const { normalizeTrainingPerformance } = await loadPerformanceModule()
  const sensitiveTokenLabel = ['to', 'ken'].join('')
  const startedAt = '2026-08-28T01:00:00Z'
  const endedAt = '2026-08-28T01:00:10Z'
  const counterSeries = (start, end) => [{
    labels: {
      pod: 'worker-a',
      dataset_id: 'labeled-full',
      dataset_version_id: 'v42',
      object_key: 'must-not-reach-the-browser',
      [sensitiveTokenLabel]: 'must-not-reach-the-browser',
    },
    points: [
      { timestamp: startedAt, value: start },
      { timestamp: endedAt, value: end },
    ],
  }]
  const normalized = normalizeTrainingPerformance({
    series: {
      datasetSourceBytesTotal: counterSeries(1_000, 5_000),
      datasetCacheBytesReadTotal: counterSeries(500, 2_500),
      datasetSourceReadsTotal: counterSeries(2, 12),
      datasetSamplesTotal: counterSeries(4, 24),
      datasetPrefetchWaitSecondsTotal: counterSeries(0.5, 2.5),
      datasetBackpressureSecondsTotal: counterSeries(0, 1),
      objectStoreSpillBytesPerSecond: [{
        labels: { pod: 'worker-a' },
        points: [
          { timestamp: startedAt, value: 100 },
          { timestamp: endedAt, value: 100 },
        ],
      }],
      objectKey: counterSeries(0, 1),
    },
    summary: {
      datasetSourceReadP95Seconds: 0.125,
      datasetCacheReadP95Seconds: 0.025,
      datasetPrefetchWaitP95Seconds: 0.075,
      datasetCacheHitsTotal: 9,
      datasetCacheMissesTotal: 1,
      datasetCacheBytesTotal: 4_096,
      datasetCacheEvictionsTotal: 2,
      datasetCacheChecksumFailuresTotal: 1,
      objectStoreSpillBytesPerSecond: 100,
      accessToken: 123,
    },
  })

  assert.equal(normalized.summary.datasetSourceBytesPerSecond, 400)
  assert.equal(normalized.summary.datasetCacheBytesPerSecond, 200)
  assert.equal(normalized.summary.datasetSourceShardsPerSecond, 1)
  assert.equal(normalized.summary.datasetSourceSamplesPerSecond, 2)
  assert.equal(normalized.summary.datasetSourceReadP95Seconds, 0.125)
  assert.equal(normalized.summary.datasetCacheReadP95Seconds, 0.025)
  assert.equal(normalized.summary.datasetPrefetchWaitP95Seconds, 0.075)
  assert.equal(normalized.summary.datasetPrefetchWaitRatio, 0.2)
  assert.equal(normalized.summary.datasetBackpressureRatio, 0.1)
  assert.equal(normalized.summary.datasetCacheHitRatio, 0.9)
  assert.equal(normalized.summary.datasetCacheBytesTotal, 4_096)
  assert.equal(normalized.summary.datasetCacheEvictionsTotal, 2)
  assert.equal(normalized.summary.datasetCacheChecksumFailuresTotal, 1)
  assert.equal(normalized.summary.objectStoreSpillBytes, 1_000)
  assert.deepEqual(normalized.series.datasetSourceBytesTotal[0].labels, {
    pod: 'worker-a',
    dataset_id: 'labeled-full',
    dataset_version_id: 'v42',
  })
  assert.equal(Object.hasOwn(normalized.series, 'objectKey'), false)
  assert.equal(Object.hasOwn(normalized.summary, 'accessToken'), false)
  assert.equal(JSON.stringify(normalized).includes('must-not-reach-the-browser'), false)
})

test('keeps derived streaming details unavailable for historical jobs with missing counters', async () => {
  const { normalizeTrainingPerformance } = await loadPerformanceModule()
  const normalized = normalizeTrainingPerformance({
    summary: {
      gpuUtilizationPercent: 0,
      datasetCacheHitsTotal: 0,
      datasetCacheMissesTotal: 0,
      objectStoreSpillBytesPerSecond: 0,
    },
  })

  assert.equal(normalized.summary.gpuUtilizationPercent, 0)
  assert.equal(normalized.summary.objectStoreSpillBytesPerSecond, 0)
  assert.equal(normalized.summary.datasetSourceBytesPerSecond, null)
  assert.equal(normalized.summary.datasetSourceShardsPerSecond, null)
  assert.equal(normalized.summary.datasetSourceReadP95Seconds, null)
  assert.equal(normalized.summary.datasetCacheReadP95Seconds, null)
  assert.equal(normalized.summary.datasetPrefetchWaitP95Seconds, null)
  assert.equal(normalized.summary.datasetPrefetchWaitRatio, null)
  assert.equal(normalized.summary.datasetBackpressureRatio, null)
  assert.equal(normalized.summary.datasetCacheHitRatio, null)
  assert.equal(normalized.summary.objectStoreSpillBytes, null)
})

test('diagnoses data wait first at the exact 20 percent boundary', async () => {
  const { diagnosePerformance } = await loadPerformanceModule()
  const diagnosis = diagnosePerformance({
    stepTimeSeconds: 10,
    dataTimeSeconds: 2,
    ncclDurationSeconds: 8,
    gpuUtilizationPercent: 10,
  })

  assert.equal(diagnosis.code, 'DATA_BOUND')
  assert.equal(diagnosis.severity, 'warning')
  assert.equal(diagnosis.ratios.data, 0.2)
  assert.match(diagnosis.advice, /数据|DataLoader/)
  assert.equal(diagnosis.dataStall.status, 'detected')
  assert.match(diagnosis.dataStall.reason, /GPU|数据/)
})

test('diagnoses communication before low GPU utilization', async () => {
  const { diagnosePerformance } = await loadPerformanceModule()
  const diagnosis = diagnosePerformance({
    stepTimeSeconds: 10,
    dataTimeSeconds: 1.9,
    ncclDurationSeconds: 2,
    gpuUtilizationPercent: 20,
  })

  assert.equal(diagnosis.code, 'COMMUNICATION_BOUND')
  assert.equal(diagnosis.severity, 'warning')
  assert.equal(diagnosis.ratios.nccl, 0.2)
})

test('diagnoses GPU underutilization and avoids invalid ratios', async () => {
  const { diagnosePerformance } = await loadPerformanceModule()
  const diagnosis = diagnosePerformance({
    stepTimeSeconds: 0,
    dataTimeSeconds: 20,
    ncclDurationSeconds: 20,
    gpuUtilizationPercent: 49.9,
  })

  assert.equal(diagnosis.code, 'GPU_UNDERUTILIZED')
  assert.equal(diagnosis.severity, 'info')
  assert.equal(diagnosis.ratios.data, null)
  assert.equal(diagnosis.ratios.nccl, null)
})

test('diagnoses balanced performance at 50 percent GPU utilization or with missing signals', async () => {
  const { diagnosePerformance } = await loadPerformanceModule()

  assert.equal(diagnosePerformance({
    stepTimeSeconds: 10,
    dataTimeSeconds: 1,
    ncclDurationSeconds: 1,
    gpuUtilizationPercent: 50,
  }).code, 'BALANCED')
  assert.equal(diagnosePerformance({
    stepTimeSeconds: 10,
    dataTimeSeconds: 0.5,
    datasetPrefetchWaitRatio: 0.05,
    datasetBackpressureRatio: 0.02,
    gpuUtilizationPercent: 85,
  }).dataStall.status, 'clear')
  assert.equal(diagnosePerformance({}).code, 'BALANCED')
  assert.equal(diagnosePerformance({}).severity, 'success')
  assert.equal(diagnosePerformance({}).dataStall.status, 'unknown')
})

test('identifies GPU data stalls from Ray prefetch or backpressure without treating missing values as zero', async () => {
  const { diagnosePerformance } = await loadPerformanceModule()

  const prefetchStall = diagnosePerformance({
    stepTimeSeconds: 4,
    dataTimeSeconds: 0.2,
    datasetPrefetchWaitRatio: 0.25,
    datasetBackpressureRatio: 0.05,
    gpuUtilizationPercent: 45,
  })
  assert.equal(prefetchStall.dataStall.status, 'detected')
  assert.equal(prefetchStall.dataStall.signal, 'prefetch')

  const backpressureStall = diagnosePerformance({
    stepTimeSeconds: 4,
    dataTimeSeconds: 0.2,
    datasetPrefetchWaitRatio: 0.05,
    datasetBackpressureRatio: 0.3,
    gpuUtilizationPercent: 45,
  })
  assert.equal(backpressureStall.dataStall.status, 'detected')
  assert.equal(backpressureStall.dataStall.signal, 'backpressure')

  assert.equal(diagnosePerformance({ gpuUtilizationPercent: 20 }).dataStall.status, 'unknown')
})

test('API wrapper delegates only the bounded authenticated same-origin path', async () => {
  const source = await read('./api/jobTrainingPerformance.js')

  assert.match(source, /import \{ apiGet \} from '\.\/client\.js'/)
  assert.match(source, /import \{ jobTrainingPerformancePath \} from '\.\.\/jobTrainingPerformance\.js'/)
  assert.match(source, /return apiGet\(jobTrainingPerformancePath\(jobId, window\)\)/)
  assert.doesNotMatch(source, /\b(?:namespace|rayClusterName|rayJobName|pod|node|selector|promql)\b/i)
})

test('performance components expose responsive accessible read-only contracts', async () => {
  const [workerTable, dataPanel, recoveryTimeline] = await Promise.all([
    read('./components/job/WorkerTable.vue'),
    read('./components/job/DataPerformancePanel.vue'),
    read('./components/job/RecoveryTimeline.vue'),
  ])

  for (const label of ['Rank', '节点', 'GPU', '状态', 'Step', 'GPU 利用率', '数据等待', '重启', 'CPU', '内存', '网络']) {
    assert.match(workerTable, new RegExp(label))
  }
  assert.match(workerTable, /overflow-x-auto/)
  assert.match(workerTable, /aria-label=/)
  assert.match(workerTable, /暂无数据/)

  for (const label of [
    '性能诊断',
    'Step 时间',
    '数据等待',
    'NCCL 通信',
    '源读取吞吐',
    '源分片速率',
    '源读取 P95',
    'Ray 预取等待',
    'Ray 背压',
    'GPU 数据停顿',
    'NVMe 有界缓存',
    '缓存读取吞吐',
    '缓存淘汰',
    '校验失败',
    'Object Spill',
    '窗口内溢写',
  ]) {
    assert.match(dataPanel, new RegExp(label))
  }
  assert.match(dataPanel, /暂无数据/)
  assert.match(dataPanel, /datasetCacheHitRatio/)
  assert.match(dataPanel, /datasetCacheBytesTotal/)
  assert.match(dataPanel, /datasetCacheReadP95Seconds/)
  assert.match(dataPanel, /datasetPrefetchWaitP95Seconds/)
  assert.match(dataPanel, /objectStoreSpillBytesPerSecond/)
  assert.doesNotMatch(dataPanel, /cacheHitsPerSecond|cacheMissesPerSecond/)
  assert.doesNotMatch(dataPanel, /object[_ -]?key|access[_ -]?token|secret[_ -]?key/i)
  assert.doesNotMatch(dataPanel, /<(?:input|button|el-input|el-slider)\b/)

  for (const label of ['集群尝试', '恢复检查点', '重启次数', '暂无恢复记录']) {
    assert.match(recoveryTimeline, new RegExp(label))
  }
  assert.match(recoveryTimeline, /<time\b/)
})

test('JobDetail integrates performance panels without replacing existing observability surfaces', async () => {
  const source = await read('./views/Job/JobDetail.vue')

  for (const contract of [
    'WorkerTable',
    'DataPerformancePanel',
    'RecoveryTimeline',
    'fetchJobTrainingPerformance',
    'normalizeTrainingPerformance',
    'diagnosePerformance',
    'TRAINING_PERFORMANCE_WINDOWS',
  ]) {
    assert.match(source, new RegExp(contract))
  }
  for (const preserved of ['Loki', 'MLflow', 'Ray Dashboard', 'GPUTrendChart', 'JobArtifactBrowser']) {
    assert.match(source, new RegExp(preserved))
  }
  assert.match(source, /watch\(selectedPerformanceWindow/)
  assert.match(source, /window\.setInterval\(refreshTrainingPerformance,\s*30000\)/)
  assert.match(source, /window\.clearInterval\(performanceRefreshTimer\)/)
  assert.match(source, /onUnmounted/)
})
