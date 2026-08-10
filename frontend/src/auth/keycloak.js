import Keycloak from 'keycloak-js'

const runtimeConfig = window.__RAY_PLATFORM_CONFIG__ || {}
const settings = {
  url: runtimeConfig.keycloakURL || import.meta.env.VITE_KEYCLOAK_URL || '',
  realm: runtimeConfig.keycloakRealm || import.meta.env.VITE_KEYCLOAK_REALM || '',
  clientId: runtimeConfig.keycloakClientID || import.meta.env.VITE_KEYCLOAK_CLIENT_ID || ''
}

const required = runtimeConfig.authRequired ?? import.meta.env.VITE_AUTH_REQUIRED === 'true'
const configured = Boolean(settings.url && settings.realm && settings.clientId)
const keycloak = required && configured ? new Keycloak(settings) : null
let initialized = false

export function isConfigured() {
  return configured
}

export async function initAuth() {
  if (initialized) return isAuthenticated()
  // When Keycloak is not configured the app still starts: the router sends the
  // user to the local login page instead of failing to boot.
  if (!keycloak) return false

  const authenticated = await keycloak.init({
    onLoad: 'check-sso',
    pkceMethod: 'S256',
    checkLoginIframe: false,
    silentCheckSsoRedirectUri: `${window.location.origin}/silent-check-sso.html`
  })
  initialized = true
  if (authenticated) {
    window.setInterval(() => {
      keycloak.updateToken(30).catch(() => {
        if (required) keycloak.login()
      })
    }, 20000)
  }
  return authenticated
}

export function isAuthenticated() {
  return Boolean(keycloak?.authenticated)
}

export async function getToken() {
  if (!keycloak?.authenticated) return null
  await keycloak.updateToken(30)
  return keycloak.token || null
}

export function currentUser() {
  if (!keycloak?.tokenParsed) return null
  const tenantGroup = (keycloak.tokenParsed.groups || []).find(group => group.startsWith('platform/tenants/'))
  return {
    username: keycloak.tokenParsed.preferred_username || '',
    email: keycloak.tokenParsed.email || '',
    groups: keycloak.tokenParsed.groups || [],
    roles: keycloak.tokenParsed.realm_access?.roles || [],
    tenantId: tenantGroup ? tenantGroup.replace('platform/tenants/', '').split('/')[0] : ''
  }
}

export function login() {
  return keycloak?.login()
}

export function logout() {
  return keycloak?.logout({ redirectUri: window.location.origin })
}

export function isAuthRequired() {
  return required
}
