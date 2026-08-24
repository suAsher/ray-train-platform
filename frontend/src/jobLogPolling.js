function cursorStrictlyAfter(timestamp) {
  const normalized = String(timestamp || '')
  const match = normalized.match(/^(.*T\d{2}:\d{2}:)(\d{2})(?:\.(\d{1,9}))?Z$/)
  if (!match) return ''

  const fraction = String(match[3] || '').padEnd(9, '0')
  const nextNanosecond = BigInt(fraction) + 1n
  if (nextNanosecond <= 999999999n) {
    return `${match[1]}${match[2]}.${String(nextNanosecond).padStart(9, '0')}Z`
  }

  const nextSecond = new Date(Date.parse(`${match[1]}${match[2]}Z`) + 1000)
  return Number.isNaN(nextSecond.getTime()) ? '' : nextSecond.toISOString()
}

function latestTimestampCursor(entries = []) {
  const timestamps = entries
    .map((entry) => String(entry?.timestamp || ''))
    .filter(Boolean)
    .sort()
  if (!timestamps.length) return ''

  return cursorStrictlyAfter(timestamps[timestamps.length - 1])
}

export function nextLogRequest(entries = [], confirmedCursor = '') {
  const cursor = String(confirmedCursor || '') || latestTimestampCursor(entries)
  if (!cursor) return { limit: 2000, direction: 'backward', cursor: '' }
  return { limit: 2000, direction: 'forward', cursor }
}

export function createSingleFlight(operation) {
  let inFlight = null
  return (...args) => {
    if (inFlight) return inFlight

    try {
      inFlight = Promise.resolve(operation(...args))
    } catch (error) {
      inFlight = Promise.reject(error)
    }
    const current = inFlight.finally(() => {
      if (inFlight === current) inFlight = null
    })
    inFlight = current
    return current
  }
}
