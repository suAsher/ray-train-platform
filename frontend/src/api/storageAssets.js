import { apiDelete, apiGet, apiPost } from './client.js'
import { storageAssetDirectoriesPath, storageAssetListPath } from './storageAssetPaths.js'

export { storageAssetDirectoriesPath, storageAssetListPath }

export function fetchStorageAssets(kind = '') {
  return apiGet(storageAssetListPath(kind))
}

export function fetchStorageDirectories(assetId, path = '', cursor = '') {
  return apiGet(storageAssetDirectoriesPath(assetId, path, cursor))
}

export function createStorageAsset(payload) {
  return apiPost('/api/v1/storage-assets', payload)
}

export function deleteStorageAsset(id) {
  return apiDelete(`/api/v1/storage-assets/${encodeURIComponent(String(id || '').trim())}`)
}
