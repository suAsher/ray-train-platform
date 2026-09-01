import { apiGet, apiPost } from './client.js'
import { datasetPath, datasetPublicationPath, datasetVersionPath, datasetVersionsPath } from './datasetPaths.js'

export { datasetPath, datasetPublicationPath, datasetVersionPath, datasetVersionsPath } from './datasetPaths.js'

export function fetchDatasets() {
  return apiGet('/api/v1/datasets')
}

export function fetchDataset(datasetId) {
  return apiGet(datasetPath(datasetId))
}

export function fetchDatasetVersions(datasetId) {
  return apiGet(datasetVersionsPath(datasetId))
}

export function fetchDatasetPublication(datasetId, versionId) {
  return apiGet(`${datasetVersionPath(datasetId, versionId)}/publication`)
}

export function fetchLatestDatasetVersion(datasetId) {
  return apiGet(`${datasetVersionsPath(datasetId)}/latest`)
}

export function createDataset(payload) {
  return apiPost('/api/v1/datasets', payload)
}

export function requestDatasetPublication(datasetId) {
  return apiPost(datasetPublicationPath(datasetId), {})
}

export function deprecateDatasetVersion(datasetId, versionId) {
  return apiPost(`${datasetVersionPath(datasetId, versionId)}/deprecate`, {})
}

export function previewDatasetGarbageCollection() {
  return apiPost('/api/v1/datasets/gc/dry-run', {})
}
