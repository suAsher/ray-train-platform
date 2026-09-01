import { apiPost } from './client.js'
import { uploadDataSpaceFile } from './dataSpaceUpload.js'
import { sha256File, validateSourceArchive } from './sourceArtifactFile.js'

export async function createSourceArtifact(file) {
  validateSourceArchive(file)
  return apiPost('/api/v1/source-artifacts', { sha256: await sha256File(file), sizeBytes: file.size })
}

export function uploadSourceArtifact(artifact, file, options = {}) {
  return uploadDataSpaceFile(artifact, file, options)
}

export function completeSourceArtifact(artifactId) {
  return apiPost(`/api/v1/source-artifacts/${encodeURIComponent(artifactId)}/complete`, {})
}

export { sha256File, validateSourceArchive }
