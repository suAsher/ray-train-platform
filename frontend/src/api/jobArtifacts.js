import { apiDownload, apiGet } from './client.js'
import { jobArtifactDownloadPath, jobArtifactPreviewPath, jobArtifactsPath } from './jobArtifactPaths.js'

export { jobArtifactDownloadPath, jobArtifactPreviewPath, jobArtifactsPath }

export function fetchJobArtifacts(jobId, path = '', cursor = '', limit = 100) {
  return apiGet(jobArtifactsPath(jobId, path, cursor, limit))
}

export function fetchJobArtifactPreview(jobId, path) {
  return apiGet(jobArtifactPreviewPath(jobId, path))
}

// Checkpoints are streamed as binary and the API needs the bearer token, so a
// plain link cannot be used: fetch the body and hand the browser a blob.
export function downloadJobArtifact(jobId, path) {
  return apiDownload(jobArtifactDownloadPath(jobId, path))
}
