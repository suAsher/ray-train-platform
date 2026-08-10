import { apiGet } from './client'

/**
 * The backend is the authority on identity: it resolves the tenant from the
 * OIDC group claim, a personal access token, or the demo fallback. Reading it
 * from /me keeps the Portal from showing a different tenant than the API
 * actually enforces.
 */
export function fetchSession() {
  return apiGet('/api/v1/me')
}
