import { apiDelete, apiGet, apiPost } from './client'

/** Runtime environments an administrator has published. */
export function fetchImages(kind) {
  return apiGet(`/api/v1/images${kind ? `?kind=${encodeURIComponent(kind)}` : ''}`)
}

export function createImage(payload) {
  return apiPost('/api/v1/images', payload)
}

export function deleteImage(id) {
  return apiDelete(`/api/v1/images/${id}`)
}

/** Credentials for private Git hosts. The token is write-only. */
export function fetchGitCredentials() {
  return apiGet('/api/v1/git-credentials')
}

export function createGitCredential(payload) {
  return apiPost('/api/v1/git-credentials', payload)
}

export function deleteGitCredential(id) {
  return apiDelete(`/api/v1/git-credentials/${id}`)
}

export function testGitCredential(id, repositoryUrl) {
  return apiPost(`/api/v1/git-credentials/${id}/test`, { repositoryUrl })
}

/** Personal machine tokens for the native spk-rayjob client. Plaintext is returned once on creation only. */
export function fetchPersonalAccessTokens() {
  return apiGet('/api/v1/personal-access-tokens')
}

export function createPersonalAccessToken(payload) {
  return apiPost('/api/v1/personal-access-tokens', payload)
}

export function revokePersonalAccessToken(id) {
  return apiDelete(`/api/v1/personal-access-tokens/${id}`)
}

export function createTenant(payload) {
  return apiPost('/api/v1/tenants', payload)
}

/**
 * Reallocate a team's GPU budget. The value is enforced on every submission,
 * so the change takes effect on the next job without a redeploy.
 */
export function setTenantGPUQuota(tenantId, gpuQuota) {
  return apiPost(`/api/v1/tenants/${encodeURIComponent(tenantId)}/quota`, { gpuQuota })
}
