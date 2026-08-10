import { isAuthenticated } from './index'
import { isAdmin, sessionLoaded, loadSession } from '../stores/session'

export function installAuthGuard(router) {
  router.beforeEach(async (to) => {
    if (to.meta?.public) {
      return isAuthenticated() && to.path === '/login' ? '/job' : true
    }
    if (!isAuthenticated()) {
      return { path: '/login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
    }
    // Roles come from the server. Resolve them before deciding, and treat an
    // unresolved session as non-admin so admin screens are never reachable on
    // a failed or pending lookup.
    if (!sessionLoaded.value) {
      await loadSession()
    }
    if (to.meta?.admin && !isAdmin.value) {
      return '/job'
    }
    return true
  })
}
