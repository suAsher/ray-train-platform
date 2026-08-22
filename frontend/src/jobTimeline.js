export function originLabel(origin) {
  return {
    portal: '网页提交',
    'ray-cli': 'spk-rayjob 外部提交',
    api: 'API 提交',
  }[origin] || '平台提交'
}

export function finishedLabel(value) {
  return value ? formatDateTime(value) : '—'
}

export function formatDateTime(value) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? '—' : date.toLocaleString('zh-CN', { hour12: false })
}

export function formatDuration(start, finish) {
  const seconds = durationSeconds(start, finish)
  if (seconds === null) return '—'
  return formatSeconds(seconds)
}

/** Seconds between two timestamps, or null when either is unusable. */
export function durationSeconds(start, finish) {
  const startAt = new Date(start ?? '').valueOf()
  const finishAt = new Date(finish ?? '').valueOf()
  if (!Number.isFinite(startAt) || !Number.isFinite(finishAt) || finishAt < startAt) return null
  return Math.floor((finishAt - startAt) / 1000)
}

export function formatSeconds(totalSeconds) {
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours} 时 ${String(minutes).padStart(2, '0')} 分`
  return `${minutes} 分 ${String(seconds).padStart(2, '0')} 秒`
}

/**
 * Split a job's lifetime into the two spans a user actually reasons about.
 *
 * Submission time and training time used to be conflated into one "耗时"
 * measured from submission, so a job that waited an hour for a GPU and then
 * trained for two minutes reported an hour. Queue wait and training time are
 * different facts and are reported separately.
 */
export function jobTimeline(job, now = new Date().toISOString()) {
  const submittedAt = job?.createdAt || job?.created_at || null
  const startedAt = job?.startedAt || job?.started_at || null
  const finishedAt = job?.finishedAt || job?.finished_at || null

  const queuedFor = startedAt
    ? durationSeconds(submittedAt, startedAt)
    : finishedAt
      ? null // finished without ever reporting a start: queue time is unknown
      : durationSeconds(submittedAt, now)

  const ranFor = startedAt ? durationSeconds(startedAt, finishedAt || now) : null

  return {
    submittedAt,
    startedAt,
    finishedAt,
    queuedSeconds: queuedFor,
    runningSeconds: ranFor,
    queuedLabel: queuedFor === null ? '—' : formatSeconds(queuedFor),
    runningLabel: ranFor === null ? '—' : formatSeconds(ranFor),
    // Still running once it has started and has no end yet.
    isRunning: Boolean(startedAt) && !finishedAt,
    // Waiting for admission: submitted, but the workload never started.
    isWaiting: !startedAt && !finishedAt,
  }
}
