const TOKEN_KEY = 'ray-platform.session'

let cached = null

function readStored() {
  if (cached) return cached
  try {
    const raw = window.localStorage.getItem(TOKEN_KEY)
    cached = raw ? JSON.parse(raw) : null
  } catch {
    cached = null
  }
  return cached
}

function persist(session) {
  cached = session
  try {
    if (session) window.localStorage.setItem(TOKEN_KEY, JSON.stringify(session))
    else window.localStorage.removeItem(TOKEN_KEY)
  } catch {
    // A browser with storage disabled still works for the current page load.
  }
}

/** Returns the stored token, dropping it once it has expired. */
export function localToken() {
  const session = readStored()
  if (!session?.token) return null
  if (session.expiresAt && Date.parse(session.expiresAt) <= Date.now()) {
    persist(null)
    return null
  }
  return session.token
}

export function localUser() {
  const session = readStored()
  if (!session?.token) return null
  return { username: session.username, tenantId: session.tenantId, roles: session.roles || [] }
}

export async function loginWithPassword(username, password) {
  const response = await fetch('/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ username, password })
  })
  const body = await response.json().catch(() => null)
  if (!response.ok || body?.success === false) {
    const error = new Error(body?.error?.message || `登录失败 (${response.status})`)
    error.code = body?.error?.code
    throw error
  }
  persist(body.data)
  return body.data
}

export async function logoutLocal() {
  const token = localToken()
  persist(null)
  if (!token) return
  try {
    await fetch('/api/v1/auth/logout', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` }
    })
  } catch {
    // The token is already discarded client-side; a failed revoke is not fatal.
  }
}

// A successful password change revokes every server-side local session. Drop
// the browser copy immediately as well, so the user must sign in with the new
// password instead of continuing with a token that is about to return 401.
export async function changeLocalPassword(currentPassword, newPassword) {
  const token = localToken()
  if (!token) {
    const error = new Error('本地登录已失效，请重新登录')
    error.code = 'LOCAL_SESSION_REQUIRED'
    throw error
  }
  const response = await fetch('/api/v1/auth/password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({ currentPassword, newPassword })
  })
  const body = await response.json().catch(() => null)
  if (!response.ok || body?.success === false) {
    const error = new Error(body?.error?.message || `修改密码失败 (${response.status})`)
    error.code = body?.error?.code
    throw error
  }
  persist(null)
  return body?.data || { updated: true }
}

export function clearLocalSession() {
  persist(null)
}

export async function fetchAuthProviders() {
  try {
    const response = await fetch('/api/v1/auth/providers', { headers: { Accept: 'application/json' } })
    const body = await response.json()
    return body?.data || { local: false, oidc: false }
  } catch {
    return { local: false, oidc: false }
  }
}
