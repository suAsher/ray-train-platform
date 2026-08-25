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
  let inFlight

  const requestKey = (jobId, window) => JSON.stringify([jobId, window])

  const isCurrent = (requestGeneration, jobId, window) => (
    active &&
    requestGeneration === generation &&
    jobId === currentJobId &&
    window === currentWindow
  )

  function refresh() {
    if (!active) return
    const jobId = currentJobId
    const window = currentWindow
    const key = requestKey(jobId, window)
    if (inFlight?.key === key) return inFlight.promise
    const requestGeneration = ++generation
    const entry = { key, promise: null }
    onLoading(true)
    inFlight = entry
    let fetchPromise
    try {
      fetchPromise = Promise.resolve(fetchHistory(jobId, window))
    } catch (error) {
      fetchPromise = Promise.reject(error)
    }
    entry.promise = fetchPromise
      .then((payload) => {
        if (!isCurrent(requestGeneration, jobId, window)) return
        onHistory(normalizeHistory(payload))
        onWarning('')
      })
      .catch((error) => {
        if (!isCurrent(requestGeneration, jobId, window)) return
        onWarning(error?.message || 'Prometheus / DCGM GPU 历史暂不可用。')
      })
      .finally(() => {
        if (inFlight !== entry) return
        inFlight = undefined
        if (isCurrent(requestGeneration, jobId, window)) onLoading(false)
      })
    return entry.promise
  }

  function start(jobId, window = '1h') {
    active = true
    generation += 1
    currentJobId = String(jobId)
    currentWindow = String(window)
    inFlight = undefined
    const initialLoad = refresh()
    timerId = schedule(refresh, JOB_GPU_REFRESH_INTERVAL_MS)
    return initialLoad
  }

  function changeWindow(window) {
    const nextWindow = String(window)
    if (nextWindow !== currentWindow) {
      generation += 1
      currentWindow = nextWindow
      inFlight = undefined
    }
    return refresh()
  }

  function changeJob(jobId) {
    generation += 1
    currentJobId = String(jobId)
    inFlight = undefined
    onReset(normalizeHistory(null))
    onWarning('')
    return refresh()
  }

  function stop() {
    active = false
    generation += 1
    inFlight = undefined
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
