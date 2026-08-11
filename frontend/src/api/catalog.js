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

export function createTenant(payload) {
  return apiPost('/api/v1/tenants', payload)
}
