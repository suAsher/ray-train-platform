import { getToken } from '../auth/index.js'

export class ApiError extends Error {
  constructor(message, status, code) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

export async function apiFetch(path, options = {}) {
  const token = await getToken()
  const headers = new Headers(options.headers || {})
  headers.set('Accept', 'application/json')
  if (options.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (token) headers.set('Authorization', `Bearer ${token}`)

  const response = await fetch(path, { ...options, headers })
  const body = await response.json().catch(() => null)
  if (response.status === 401 && !path.includes('/auth/')) {
    // The session expired or was revoked; send the user back to sign in.
    window.location.assign('/login')
  }
  if (!response.ok) {
    throw new ApiError(body?.error?.message || `请求失败 (${response.status})`, response.status, body?.error?.code)
  }
  if (body?.success === false) {
    throw new ApiError(body.error?.message || '请求失败', response.status, body.error?.code)
  }
  return body?.success === true ? body.data : body
}

// apiFetch parses every response as JSON, which cannot carry a checkpoint.
// This variant keeps the same authentication and session handling but returns
// the raw body so the caller can save it as a file.
export async function apiDownload(path) {
  const token = await getToken()
  const headers = new Headers({ Accept: 'application/octet-stream' })
  if (token) headers.set('Authorization', `Bearer ${token}`)

  const response = await fetch(path, { headers })
  if (response.status === 401) {
    window.location.assign('/login')
    throw new ApiError('登录已失效', 401)
  }
  if (!response.ok) {
    const body = await response.json().catch(() => null)
    throw new ApiError(body?.error?.message || `下载失败 (${response.status})`, response.status, body?.error?.code)
  }
  return response.blob()
}

export const apiGet = (path) => apiFetch(path)
export const apiPost = (path, data, options = {}) => apiFetch(path, { ...options, method: 'POST', body: JSON.stringify(data) })
export const apiPatch = (path, data, options = {}) => apiFetch(path, { ...options, method: 'PATCH', body: JSON.stringify(data) })
export const apiDelete = (path) => apiFetch(path, { method: 'DELETE' })
