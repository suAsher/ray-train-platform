import { getToken } from '../auth/index.js'
import { uploadDataSpaceFile } from './dataSpaceUpload.js'
import { sha256File, validateSourceArchive } from './sourceArtifactFile.js'

// The header the platform uses to tell us which owner-scoped artifact it stored
// for an upload. Without it we cannot bind the package to a training job.
const ARTIFACT_ID_HEADER = 'X-Ray-Platform-Source-Artifact-ID'

// Uploads used to go straight from the browser to object storage using a
// presigned URL. That endpoint resolves to a VPC-internal address, so a browser
// outside the cluster could never reach it: the upload failed, the artifact was
// left PENDING forever, and those rows eventually exhausted the user's quota.
//
// The bytes now go through the platform, which is the only host a user is
// expected to reach. This reuses the same relay the CLI uses rather than adding
// a second upload path, so the digest check, size limit, rate limit, disk
// spooling and quota accounting are the ones already proven in production.
//
// The package name is the archive's own SHA256, which the platform recomputes
// while receiving the bytes, so a corrupted or altered upload cannot be stored.
export async function uploadSourceArchive(file, options = {}) {
  validateSourceArchive(file)
  const sha256 = await sha256File(file)
  const token = await getToken()
  const headers = { 'Content-Type': 'application/zip' }
  if (token) headers.Authorization = `Bearer ${token}`

  const request = await uploadDataSpaceFile(
    { url: `/ray/api/packages/gcs/${sha256}.zip`, headers },
    file,
    options,
  )
  const artifactId = request?.getResponseHeader?.(ARTIFACT_ID_HEADER)?.trim()
  if (!artifactId) {
    throw new Error('平台未返回代码包标识，请重试或联系管理员')
  }
  return { artifactId, sha256, sizeBytes: Number(file.size || 0) }
}

export { sha256File, validateSourceArchive }
