import { computed, reactive } from 'vue'
import { fetchSession } from '../api/session'

const ADMIN_ROLES = ['SuperAdmin', 'TenantAdmin']

/**
 * The signed-in user as the backend resolves them. Roles come from the server
 * rather than from a decoded token so the navigation a user sees matches the
 * permissions the API actually enforces.
 */
const state = reactive({
  loaded: false,
  failed: false,
  user: null
})

export async function loadSession() {
  try {
    state.user = await fetchSession()
    state.failed = false
  } catch {
    state.user = null
    state.failed = true
  } finally {
    state.loaded = true
  }
  return state.user
}

export const session = computed(() => state.user)
export const sessionLoaded = computed(() => state.loaded)
export const sessionFailed = computed(() => state.failed)

export const roles = computed(() => state.user?.roles || [])

/**
 * Admin status is deliberately fail-closed: until the session is known, the
 * user is treated as a plain engineer so administrative screens never flash
 * into view for someone who cannot use them.
 */
export const isAdmin = computed(() => roles.value.some((role) => ADMIN_ROLES.includes(role)))

export const tenantId = computed(() => state.user?.tenantId || '')
export const userId = computed(() => state.user?.subject || '')
export const isDemoSession = computed(() => state.user?.anonymous === true)
