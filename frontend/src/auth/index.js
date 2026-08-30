/**
 * Unified authentication facade.
 *
 * The Portal supports two interchangeable login methods: a built-in local
 * account (username/password, no external dependency) and Keycloak/OIDC. The
 * rest of the app talks to this module so no view needs to know which one is
 * active.
 */
import * as keycloak from './keycloak.js'
import { localToken, localUser, logoutLocal, clearLocalSession } from './localSession.js'

export { loginWithPassword, fetchAuthProviders } from './localSession.js'

export async function initAuth() {
  // A local session takes precedence: if the user signed in with a password we
  // must not bounce them to Keycloak.
  if (localToken()) return true
  return keycloak.initAuth()
}

export async function getToken() {
  const local = localToken()
  if (local) return local
  return keycloak.getToken()
}

export function isAuthenticated() {
  return Boolean(localToken()) || keycloak.isAuthenticated()
}

export function currentUser() {
  return localUser() || keycloak.currentUser()
}

export function isAuthRequired() {
  return keycloak.isAuthRequired()
}

export function isKeycloakConfigured() {
  return keycloak.isConfigured()
}

export async function logout() {
  if (localToken()) {
    await logoutLocal()
    window.location.assign('/login')
    return
  }
  clearLocalSession()
  return keycloak.logout()
}
