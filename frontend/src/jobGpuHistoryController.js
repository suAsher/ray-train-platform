export const JOB_GPU_REFRESH_INTERVAL_MS = 30000

const noop = () => {}

export function createJobGPUHistoryController({
  fetchHistory,
  normalizeHistory,
  onHistory = noop,
  onWarning = noop,
  onLoading = noop,
  onReset = noop,
  setInterval: schedule = globalThis.setInterval,
  clearInterval: cancel = globalThis.clearInterval,
}) {
  let active = false
  let currentJobId = ''
  let currentWindow = '1h'
  let generation = 0
  let timerId

  const isCurrent = (requestGeneration, jobId, window) => (
    active &&
    requestGeneration === generation &&
    jobId === currentJobId &&
    window === currentWindow
  )

  async function refresh() {
    if (!active) return
    const requestGeneration = ++generation
    const jobId = currentJobId
    const window = currentWindow
    onLoading(true)
    try {
      const payload = await fetchHistory(jobId, window)
      if (!isCurrent(requestGeneration, jobId, window)) return
      onHistory(normalizeHistory(payload))
      onWarning('')
    } catch (error) {
      if (!isCurrent(requestGeneration, jobId, window)) return
      onWarning(error?.message || 'Prometheus / DCGM GPU 历史暂不可用。')
    } finally {
      if (isCurrent(requestGeneration, jobId, window)) onLoading(false)
    }
  }

  function start(jobId, window = '1h') {
    active = true
    generation += 1
    currentJobId = String(jobId)
    currentWindow = String(window)
    const initialLoad = refresh()
    timerId = schedule(refresh, JOB_GPU_REFRESH_INTERVAL_MS)
    return initialLoad
  }

  function changeWindow(window) {
    currentWindow = String(window)
    return refresh()
  }

  function changeJob(jobId) {
    generation += 1
    currentJobId = String(jobId)
    onReset(normalizeHistory(null))
    onWarning('')
    return refresh()
  }

  function stop() {
    active = false
    generation += 1
    if (timerId !== undefined) cancel(timerId)
    timerId = undefined
    onLoading(false)
  }

  return {
    start,
    refresh,
    changeWindow,
    changeJob,
    stop,
  }
}
