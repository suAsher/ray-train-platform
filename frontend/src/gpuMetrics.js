export const GPU_TIME_WINDOWS = Object.freeze([
  Object.freeze({ value: '15m', label: '15 分钟' }),
  Object.freeze({ value: '1h', label: '1 小时' }),
  Object.freeze({ value: '6h', label: '6 小时' }),
  Object.freeze({ value: '24h', label: '24 小时' }),
  Object.freeze({ value: '7d', label: '7 天' }),
])

export function jobGPUHistoryPath(jobId, window = '1h') {
  const selectedWindow = GPU_TIME_WINDOWS.some(({ value }) => value === window) ? window : '1h'
  return `/api/v1/jobs/${encodeURIComponent(String(jobId ?? ''))}/gpu-metrics?window=${encodeURIComponent(selectedWindow)}`
}

const METRICS = Object.freeze(['utilizationPercent', 'memoryUsedMib', 'temperatureCelsius', 'powerWatts'])
const safeText = (value) => typeof value === 'string' ? value.trim() : ''
const finiteNumber = (value) => Number.isFinite(Number(value)) ? Number(value) : null

function normalizePoints(value) {
  if (!Array.isArray(value)) return []
  return value.flatMap((point) => {
    const date = new Date(point?.timestamp || '')
    const metricValue = finiteNumber(point?.value)
    if (!Number.isFinite(date.getTime()) || metricValue === null) return []
    return [{ timestamp: date.toISOString(), value: metricValue }]
  })
}

export function normalizeGPUHistory(payload) {
  const source = payload && typeof payload === 'object' ? payload : {}
  const devices = Array.isArray(source.devices) ? source.devices : []
  return {
    window: safeText(source.window),
    stepSeconds: Math.max(0, Math.trunc(finiteNumber(source.stepSeconds) || 0)),
    startedAt: safeText(source.startedAt),
    endedAt: safeText(source.endedAt),
    devices: devices.map((device) => ({
      uuid: safeText(device?.uuid),
      nodeName: safeText(device?.nodeName),
      index: safeText(device?.index),
      model: safeText(device?.model),
      namespace: safeText(device?.namespace),
      podName: safeText(device?.podName),
      containerName: safeText(device?.containerName),
      series: Object.fromEntries(METRICS.map((metric) => [metric, normalizePoints(device?.series?.[metric])])),
    })),
  }
}

export function sampleFreshness(value, now = new Date()) {
  const sampledAt = new Date(value || '')
  const current = now instanceof Date ? now : new Date(now)
  if (!Number.isFinite(sampledAt.getTime()) || !Number.isFinite(current.getTime())) {
    return { stale: true, ageSeconds: null }
  }
  const ageSeconds = Math.max(0, Math.floor((current.getTime() - sampledAt.getTime()) / 1000))
  return { stale: ageSeconds > 90, ageSeconds }
}

const average = (values) => values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : 0
const latest = (points) => points.length ? points[points.length - 1].value : 0
const recentValues = (points, seconds = 60) => {
  if (!points.length) return []
  const latestTime = new Date(points[points.length - 1].timestamp).getTime()
  return points
    .filter((point) => latestTime - new Date(point.timestamp).getTime() <= seconds * 1000)
    .map((point) => point.value)
}

function summarizeDevices(devices) {
  const utilizationByDevice = devices.map((device) => average(recentValues(device.series.utilizationPercent)))
  const utilizationSpread = utilizationByDevice.length
    ? Math.max(...utilizationByDevice) - Math.min(...utilizationByDevice)
    : 0
  return {
    averageUtilizationPercent: Math.round(average(utilizationByDevice)),
    totalMemoryUsedMib: devices.reduce((sum, device) => sum + latest(device.series.memoryUsedMib), 0),
    totalPowerWatts: Math.round(devices.reduce((sum, device) => sum + latest(device.series.powerWatts), 0)),
    maximumTemperatureCelsius: Math.round(Math.max(0, ...devices.map((device) => latest(device.series.temperatureCelsius)))),
    utilizationSpread: Math.round(utilizationSpread),
    imbalanced: utilizationByDevice.length > 1 && utilizationSpread >= 30,
  }
}

function validMetricPoints(device, metric) {
  if (!Array.isArray(device?.series?.[metric])) return []
  return device.series[metric].flatMap((point) => {
    const timestamp = new Date(point?.timestamp || '')
    const value = finiteNumber(point?.value)
    if (!Number.isFinite(timestamp.getTime()) || value === null) return []
    return [{ timestamp: timestamp.toISOString(), value }]
  })
}

function sampledMetricSeries(devices, metric) {
  return devices.flatMap((device) => {
    const points = validMetricPoints(device, metric)
    return points.length ? [points] : []
  })
}

function metricCoverage(seriesByMetric, total) {
  return Object.freeze(Object.fromEntries(METRICS.map((metric) => [
    metric,
    Object.freeze({ sampled: seriesByMetric[metric].length, total }),
  ])))
}

export function nodeMetricSummary(history, nodeName) {
  const devices = (history?.devices || []).filter((device) => device.nodeName === nodeName)
  return summarizeDevices(devices)
}

export function jobMetricSummary(history) {
  const devices = [...(history?.devices || [])]
  const seriesByMetric = Object.fromEntries(METRICS.map((metric) => [metric, sampledMetricSeries(devices, metric)]))
  const utilizationSeries = seriesByMetric.utilizationPercent
  const memorySeries = seriesByMetric.memoryUsedMib
  const powerSeries = seriesByMetric.powerWatts
  const temperatureSeries = seriesByMetric.temperatureCelsius
  const utilizationByDevice = utilizationSeries.map((points) => average(recentValues(points)))
  const utilizationSpread = utilizationByDevice.length
    ? Math.max(...utilizationByDevice) - Math.min(...utilizationByDevice)
    : null
  return {
    deviceCount: devices.length,
    averageUtilizationPercent: utilizationByDevice.length ? Math.round(average(utilizationByDevice)) : null,
    totalMemoryUsedMib: memorySeries.length ? memorySeries.reduce((sum, points) => sum + latest(points), 0) : null,
    totalPowerWatts: powerSeries.length ? Math.round(powerSeries.reduce((sum, points) => sum + latest(points), 0)) : null,
    maximumTemperatureCelsius: temperatureSeries.length
      ? Math.round(Math.max(...temperatureSeries.map((points) => latest(points))))
      : null,
    utilizationSpread: utilizationSpread === null ? null : Math.round(utilizationSpread),
    imbalanced: utilizationByDevice.length > 1 && utilizationSpread >= 30,
    coverage: metricCoverage(seriesByMetric, devices.length),
  }
}

export function metricChartSeries(history, nodeName, metric) {
  if (!METRICS.includes(metric)) return []
  return (history?.devices || [])
    .filter((device) => device.nodeName === nodeName)
    .sort((left, right) => Number(left.index) - Number(right.index))
    .map((device) => ({
      id: device.uuid,
      name: `GPU ${device.index}`,
      data: device.series[metric].map((point) => [point.timestamp, point.value]),
    }))
}

export function jobMetricChartSeries(history, metric) {
  if (!METRICS.includes(metric)) return []
  return [...(history?.devices || [])]
    .filter((device) => device?.series?.[metric]?.length)
    .sort((left, right) => {
      const nodeComparison = left.nodeName.localeCompare(right.nodeName)
      if (nodeComparison !== 0) return nodeComparison
      const leftIndex = safeText(left.index)
      const rightIndex = safeText(right.index)
      const leftNumericIndex = Number(leftIndex)
      const rightNumericIndex = Number(rightIndex)
      const bothIndexesNumeric = leftIndex !== '' && rightIndex !== ''
        && Number.isFinite(leftNumericIndex) && Number.isFinite(rightNumericIndex)
      const indexComparison = bothIndexesNumeric
        ? leftNumericIndex - rightNumericIndex
        : leftIndex.localeCompare(rightIndex)
      if (indexComparison !== 0) return indexComparison
      return left.uuid.localeCompare(right.uuid)
    })
    .map((device) => ({
      id: device.uuid,
      name: `${device.nodeName || '未知节点'} / GPU ${device.index}`,
      data: device.series[metric].map((point) => [point.timestamp, point.value]),
    }))
}

export function recentDeviceAverage(history, uuid, metric = 'utilizationPercent') {
  if (!METRICS.includes(metric)) return null
  const device = (history?.devices || []).find((item) => item.uuid === uuid)
  if (!device || !device.series[metric].length) return null
  return average(recentValues(device.series[metric]))
}
