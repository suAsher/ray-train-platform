import { getToken } from '../auth/index.js'
import { apiDownload, apiGet, apiPost } from './client.js'
import { dataSpaceDirectoriesPath, dataSpaceDownloadPath, dataSpaceEntriesPath, dataSpacesPath, workspaceSnapshotsPath } from './dataSpacesPaths.js'
import { uploadDataSpaceFile } from './dataSpaceUpload.js'

export { dataSpaceDirectoriesPath, dataSpaceDownloadPath, dataSpaceEntriesPath, dataSpacesPath, workspaceSnapshotsPath }

export function fetchDataSpaces() {
  return apiGet(dataSpacesPath())
}

export function fetchDataSpaceDirectories(spaceId, path = '', cursor = '') {
  return apiGet(dataSpaceDirectoriesPath(spaceId, path, cursor))
}

export function fetchDataSpaceEntries(spaceId, path = '', cursor = '') {
  return apiGet(dataSpaceEntriesPath(spaceId, path, cursor))
}

export function createDataSpaceFolder(spaceId, path) {
  return apiPost(`/api/v1/data-spaces/${encodeURIComponent(spaceId)}/folders`, { path })
}

// The platform now relays these bytes instead of handing out a presigned
// object-store URL, because object storage resolves to a VPC-internal address
// that a browser outside the cluster can never reach. The ticket therefore
// points back at the platform's own authenticated API and needs credentials,
// which a presigned URL must never receive.
export async function createDataSpaceUpload(spaceId, path, contentType, sizeBytes) {
  const ticket = await apiPost(`/api/v1/data-spaces/${encodeURIComponent(spaceId)}/uploads`, { path, contentType, sizeBytes })
  const token = await getToken()
  const headers = { 'Content-Type': ticket.contentType || contentType || 'application/octet-stream' }
  if (token) headers.Authorization = `Bearer ${token}`
  return { ...ticket, spaceId, headers }
}

export function fetchWorkspaceSnapshots(limit = 50) {
  return apiGet(workspaceSnapshotsPath(limit))
}

export function createWorkspaceSnapshot(sourcePath = '') {
  return apiPost('/api/v1/workspace-snapshots', { sourcePath })
}

export { uploadDataSpaceFile }

// Checkpoints are binary and the API needs the bearer token, so a plain link
// cannot be used: fetch the body and hand the browser a blob.
export function downloadDataSpaceFile(spaceId, path) {
  return apiDownload(dataSpaceDownloadPath(spaceId, path))
}
