export function dataSpacesPath() {
  return '/api/v1/data-spaces'
}

export function dataSpaceDirectoriesPath(spaceId, path = '', cursor = '') {
	return dataSpacePath(spaceId, 'directories', path, cursor)
}

export function dataSpaceEntriesPath(spaceId, path = '', cursor = '') {
	return dataSpacePath(spaceId, 'entries', path, cursor)
}

export function dataSpaceDownloadPath(spaceId, path = '') {
	return dataSpacePath(spaceId, 'download', path)
}

export function workspaceSnapshotsPath(limit = '') {
  const query = new URLSearchParams()
  if (String(limit || '').trim()) query.set('limit', String(limit).trim())
  const encoded = query.toString()
  return `/api/v1/workspace-snapshots${encoded ? `?${encoded}` : ''}`
}

function dataSpacePath(spaceId, resource, path = '', cursor = '') {
  const query = new URLSearchParams()
  if (String(path || '').trim()) query.set('path', String(path).trim())
  if (String(cursor || '')) query.set('cursor', String(cursor))
  const encoded = query.toString()
  return `/api/v1/data-spaces/${encodeURIComponent(String(spaceId || '').trim())}/${resource}${encoded ? `?${encoded}` : ''}`
}
