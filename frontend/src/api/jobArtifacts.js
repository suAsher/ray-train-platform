import { apiGet } from './client.js'
import { jobArtifactPreviewPath, jobArtifactsPath } from './jobArtifactPaths.js'

export { jobArtifactPreviewPath, jobArtifactsPath }

export function fetchJobArtifacts(jobId, path = '', cursor = '', limit = 100) {
  return apiGet(jobArtifactsPath(jobId, path, cursor, limit))
}

export function fetchJobArtifactPreview(jobId, path) {
  return apiGet(jobArtifactPreviewPath(jobId, path))
}
