const maxMetadataBytes = 65536

const releaseNumber = (value) => {
  const match = typeof value === 'string' && /^release-(\d{4})(\d{2})(\d{2})-(\d{1,9})$/.exec(value)
  if (!match) throw new Error('客户端版本清单格式不正确')
  const [, year, month, day, sequence] = match
  const date = new Date(`${year}-${month}-${day}T00:00:00Z`)
  if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 10) !== `${year}-${month}-${day}` || Number(sequence) < 1) {
    throw new Error('客户端版本清单格式不正确')
  }
  return [Number(`${year}${month}${day}`), Number(sequence)]
}

export function normalizeCLIRelease(value) {
  if (value?.schemaVersion !== 1 || typeof value.minimumVersion !== 'string' || typeof value.releaseNotes !== 'string' || value.releaseNotes.length > 8000) {
    throw new Error('客户端版本清单格式不正确')
  }
  const latest = releaseNumber(value.latestVersion)
  if (value.minimumVersion) {
    const minimum = releaseNumber(value.minimumVersion)
    if (minimum[0] > latest[0] || (minimum[0] === latest[0] && minimum[1] > latest[1])) throw new Error('最低版本高于最新版本')
  }
  return { schemaVersion: 1, latestVersion: value.latestVersion, minimumVersion: value.minimumVersion, releaseNotes: value.releaseNotes }
}

export async function fetchCLIRelease(fetcher = fetch, signal) {
  const controller = new AbortController()
  const abort = () => controller.abort()
  signal?.addEventListener('abort', abort, { once: true })
  if (signal?.aborted) abort()
  const timer = setTimeout(abort, 3000)
  let reader
  try {
    const response = await fetcher('/downloads/spk-rayjob/release.json', { credentials: 'omit', redirect: 'error', cache: 'no-store', signal: controller.signal })
    if (!response.ok) throw new Error('暂时无法获取客户端版本，请重试或使用安装命令')
    reader = response.body.getReader()
    const chunks = []
    let length = 0
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      length += value.length
      if (length > maxMetadataBytes) throw new Error('客户端版本清单过大')
      chunks.push(value)
    }
    const bytes = new Uint8Array(length)
    let offset = 0
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length }
    return normalizeCLIRelease(JSON.parse(new TextDecoder().decode(bytes)))
  } finally {
    await reader?.cancel().catch(() => {})
    clearTimeout(timer)
    signal?.removeEventListener('abort', abort)
  }
}
