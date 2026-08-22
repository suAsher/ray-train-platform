export function storageAssetListPath(kind = '') {
  const query = new URLSearchParams()
  if (String(kind || '').trim()) query.set('kind', String(kind).trim())
  const encoded = query.toString()
  return `/api/v1/storage-assets${encoded ? `?${encoded}` : ''}`
}

export function storageAssetDirectoriesPath(assetId, path = '', cursor = '') {
  const query = new URLSearchParams()
  if (String(path || '').trim()) query.set('path', String(path).trim())
  if (String(cursor || '')) query.set('cursor', String(cursor))
  const encoded = query.toString()
  return `/api/v1/storage-assets/${encodeURIComponent(String(assetId || '').trim())}/directories${encoded ? `?${encoded}` : ''}`
}
