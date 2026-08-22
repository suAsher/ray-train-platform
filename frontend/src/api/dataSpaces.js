import { apiGet, apiPost } from './client.js'
import { dataSpaceDirectoriesPath, dataSpaceEntriesPath, dataSpacesPath, workspaceSnapshotsPath } from './dataSpacesPaths.js'
import { uploadDataSpaceFile } from './dataSpaceUpload.js'

export { dataSpaceDirectoriesPath, dataSpaceEntriesPath, dataSpacesPath, workspaceSnapshotsPath }

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

export function createDataSpaceUpload(spaceId, path, contentType, sizeBytes) {
  return apiPost(`/api/v1/data-spaces/${encodeURIComponent(spaceId)}/uploads`, { path, contentType, sizeBytes })
}

export function fetchWorkspaceSnapshots(limit = 50) {
  return apiGet(workspaceSnapshotsPath(limit))
}

export function createWorkspaceSnapshot(sourcePath = '') {
  return apiPost('/api/v1/workspace-snapshots', { sourcePath })
}

export { uploadDataSpaceFile }
