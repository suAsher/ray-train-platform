function artifactQuery(path = '', cursor = '', limit = 0) {
  const query = new URLSearchParams()
  if (String(path || '').trim()) query.set('path', String(path).trim())
  if (String(cursor || '').trim()) query.set('cursor', String(cursor).trim())
  if (Number.isInteger(limit) && limit > 0) query.set('limit', String(limit))
  const encoded = query.toString()
  return encoded ? `?${encoded}` : ''
}

function jobPath(jobId) {
  return `/api/v1/jobs/${encodeURIComponent(String(jobId || '').trim())}/artifacts`
}

export function jobArtifactsPath(jobId, path = '', cursor = '', limit = 0) {
  return `${jobPath(jobId)}${artifactQuery(path, cursor, limit)}`
}

export function jobArtifactPreviewPath(jobId, path) {
  return `${jobPath(jobId)}/preview${artifactQuery(path)}`
}
