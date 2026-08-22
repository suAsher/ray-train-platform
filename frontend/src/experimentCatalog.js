const METRIC_DEFINITIONS = [
  { key: 'loss', label: 'Loss', candidates: ['train_loss', 'training_loss', 'loss'] },
  { key: 'learning-rate', label: '学习率', candidates: ['learning_rate', 'learning-rate', 'lr'] },
  { key: 'epoch', label: 'Epoch', candidates: ['epoch', 'current_epoch'] },
  { key: 'map', label: 'mAP', candidates: ['mAP', 'map', 'mean_average_precision'] },
  { key: 'nds', label: 'NDS', candidates: ['NDS', 'nds'] },
]

const STATUS_PRESENTATIONS = {
  RUNNING: { label: '运行中', type: 'success' },
  SCHEDULED: { label: '等待中', type: 'warning' },
  FINISHED: { label: '已完成', type: 'success' },
  FAILED: { label: '失败', type: 'danger' },
  KILLED: { label: '已终止', type: 'warning' },
}

const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value)

const firstText = (...values) => {
  const match = values.find((value) => typeof value === 'string' && value.trim())
  return match?.trim() || ''
}

const metricNumber = (value) => {
  if (value === null || value === undefined || value === '') return null
  const number = Number(value)
  return Number.isFinite(number) ? number : null
}

const timestampMs = (value) => {
  if (value === null || value === undefined || value === '') return null
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (typeof value === 'string' && /^\d+(\.\d+)?$/.test(value.trim())) {
    const number = Number(value)
    return Number.isFinite(number) ? number : null
  }
  const parsed = new Date(value).valueOf()
  return Number.isFinite(parsed) ? parsed : null
}

const formatSeconds = (totalSeconds) => {
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours} 时 ${String(minutes).padStart(2, '0')} 分`
  return `${minutes} 分 ${String(seconds).padStart(2, '0')} 秒`
}

function catalogItems(payload) {
  if (Array.isArray(payload)) return payload
  if (!isObject(payload)) return []
  if (Array.isArray(payload.items)) return payload.items
  if (Array.isArray(payload.experiments)) return payload.experiments
  if (Array.isArray(payload.runs)) {
    return payload.runs.map((run) => isObject(run)
      ? { ...run, experimentName: firstText(run.experimentName, payload.experimentName) }
      : run)
  }
  return payload.data === payload ? [] : catalogItems(payload.data)
}

export function keyMetrics(latest) {
  const values = isObject(latest) ? latest : {}
  return METRIC_DEFINITIONS.flatMap((definition) => {
    for (const candidate of definition.candidates) {
      if (!Object.hasOwn(values, candidate)) continue
      const value = metricNumber(values[candidate])
      if (value !== null) return [{ key: definition.key, label: definition.label, value }]
    }
    return []
  })
}

function normalizeExperiment(item) {
  const source = isObject(item) ? item : {}
  const run = isObject(source.run) ? source.run : source
  const job = isObject(source.job) ? source.job : {}
  const params = isObject(run.params) ? run.params : (isObject(source.params) ? source.params : {})
  const latest = isObject(run.latest)
    ? run.latest
    : (isObject(source.latestMetrics) ? source.latestMetrics : (isObject(source.latest) ? source.latest : {}))
  const jobId = firstText(
    source.jobId,
    source.jobID,
    job.id,
    params['platform.job_id'],
    params.job_id,
  )

  return {
    experimentName: firstText(source.experimentName, source.experiment?.name),
    jobId,
    jobName: firstText(
      source.jobName,
      source.displayName,
      job.displayName,
      job.name,
      params['platform.job_name'],
      params.job_name,
      jobId,
    ),
    runId: firstText(run.id, run.runId, run.run_id),
    runName: firstText(run.name, run.runName, run.run_name, run.id, run.runId, run.run_id),
    status: firstText(run.status, source.status) || 'UNKNOWN',
    startTimeMs: timestampMs(run.startTimeMs ?? run.startTime ?? run.start_time),
    endTimeMs: timestampMs(run.endTimeMs ?? run.endTime ?? run.end_time),
    metrics: keyMetrics(latest),
  }
}

export function normalizeExperimentCatalog(payload) {
  return catalogItems(payload).map(normalizeExperiment)
}

export async function fetchExperimentCatalog(request) {
  const fetcher = request || (await import('./api/client.js')).apiGet
  const payload = await fetcher('/api/v1/experiments')
  return normalizeExperimentCatalog(payload)
}

export function statusPresentation(status) {
  const rawStatus = typeof status === 'string' ? status.trim() : ''
  return STATUS_PRESENTATIONS[rawStatus.toUpperCase()] || { label: rawStatus || '未知', type: 'info' }
}

export function formatExperimentTime(value) {
  const valueMs = timestampMs(value)
  if (valueMs === null) return '—'
  return new Date(valueMs).toLocaleString('zh-CN', { hour12: false })
}

export function formatExperimentDuration(start, end, now = Date.now()) {
  const startMs = timestampMs(start)
  const endMs = timestampMs(end) ?? timestampMs(now)
  if (startMs === null || endMs === null || endMs < startMs) return '—'
  return formatSeconds(Math.floor((endMs - startMs) / 1000))
}

export function formatMetricValue(value) {
  const number = metricNumber(value)
  if (number === null) return '—'
  if (Number.isInteger(number)) return String(number)
  const magnitude = Math.abs(number)
  if (magnitude > 0 && magnitude < 0.001) return number.toExponential(2)
  return number.toFixed(magnitude < 1 ? 4 : 3).replace(/\.?0+$/, '')
}
