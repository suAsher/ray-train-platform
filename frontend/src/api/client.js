import { getToken } from '../auth'

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

export const apiGet = (path) => apiFetch(path)
export const apiPost = (path, data, options = {}) => apiFetch(path, { ...options, method: 'POST', body: JSON.stringify(data) })
export const apiDelete = (path) => apiFetch(path, { method: 'DELETE' })
