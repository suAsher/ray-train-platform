function streamKey(stream = {}) {
  return Object.keys(stream).sort().map((key) => `${key}=${stream[key]}`).join('|')
}

function entryKey(entry) {
  return `${entry.timestamp || ''}\u0000${streamKey(entry.stream)}\u0000${entry.text || ''}`
}

export function normalizeLogPage(payload = {}) {
  const items = Array.isArray(payload.items) ? payload.items : []
  const page = payload.page || {}
  return {
    logs: items.map((item) => ({
      node: item.stream?.pod || item.stream?.container || 'cluster',
      text: String(item.line || ''),
      timestamp: String(item.timestamp || ''),
      stream: { ...(item.stream || {}) },
    })),
    hasMore: Boolean(page.hasMore),
    nextCursor: String(page.nextCursor || ''),
  }
}

export function mergeLogEntries(current = [], incoming = []) {
  const entries = new Map()
  for (const entry of [...current, ...incoming]) entries.set(entryKey(entry), { ...entry, stream: { ...(entry.stream || {}) } })
  return [...entries.values()].sort((left, right) => {
    const timestampOrder = String(left.timestamp || '').localeCompare(String(right.timestamp || ''))
    return timestampOrder || entryKey(left).localeCompare(entryKey(right))
  })
}

export function logPagePath(jobId, { limit = 2000, direction = 'backward', cursor = '' } = {}) {
  const normalizedDirection = direction === 'forward' ? 'forward' : 'backward'
  const query = new URLSearchParams({ limit: String(limit), direction: normalizedDirection })
  if (cursor) query.set(normalizedDirection === 'forward' ? 'after' : 'before', cursor)
  return `/api/v1/jobs/${encodeURIComponent(jobId)}/logs?${query.toString()}`
}
