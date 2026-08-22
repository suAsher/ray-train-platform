export function latestMetric(experiment, candidates) {
  const latest = experiment?.run?.latest || {}
  for (const key of candidates) {
    const value = latest[key]
    if (Number.isFinite(Number(value))) return Number(value)
  }
  return null
}

export function metricSeries(experiment, candidates) {
  const series = Array.isArray(experiment?.series) ? experiment.series : []
  for (const key of candidates) {
    const match = series.find(item => item?.key === key)
    if (match) return match
  }
  return null
}

export function sparklinePoints(points, width = 640, height = 220) {
  const clean = (Array.isArray(points) ? points : [])
    .map(point => ({ step: Number(point?.step), value: Number(point?.value) }))
    .filter(point => Number.isFinite(point.step) && Number.isFinite(point.value))
  if (!clean.length) return ''
  if (clean.length === 1) return `0,${height / 2}`
  const minimum = Math.min(...clean.map(point => point.value))
  const maximum = Math.max(...clean.map(point => point.value))
  const range = maximum - minimum || 1
  return clean.map((point, index) => {
    const x = (index / (clean.length - 1)) * width
    const y = ((maximum - point.value) / range) * height
    return `${Number(x.toFixed(2))},${Number(y.toFixed(2))}`
  }).join(' ')
}
