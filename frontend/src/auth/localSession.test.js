import assert from 'node:assert/strict'
import test from 'node:test'

const values = new Map()
globalThis.window = {
  localStorage: {
    getItem: (key) => values.get(key) || null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key)
  }
}

const session = await import('./localSession.js')

test('changeLocalPassword authenticates the request and clears the old local session', async () => {
  session.clearLocalSession()
  const calls = []
  globalThis.fetch = async (path, options = {}) => {
    calls.push({ path, options })
    if (path === '/api/v1/auth/login') {
      return new Response(JSON.stringify({ success: true, data: { token: 'rls_abcdefghijklmnopqrstuvwx_abcdefghijklmnopqrstuvwxyz0123456789', expiresAt: '2099-01-01T00:00:00Z', username: 'alice', tenantId: 'team-a', roles: ['Engineer'] } }), { status: 200 })
    }
    return new Response(JSON.stringify({ success: true, data: { updated: true } }), { status: 200 })
  }

  await session.loginWithPassword('alice', 'correct-horse')
  await session.changeLocalPassword('correct-horse', 'new-correct-horse')

  assert.equal(calls[1].path, '/api/v1/auth/password')
  assert.equal(calls[1].options.headers.Authorization.startsWith('Bearer rls_'), true)
  assert.deepEqual(JSON.parse(calls[1].options.body), { currentPassword: 'correct-horse', newPassword: 'new-correct-horse' })
  assert.equal(session.localToken(), null)
})
