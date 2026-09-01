const positiveInteger = (value, fallback) => Number.isInteger(value) && value > 0 ? value : fallback
const nonNegativeInteger = (value, fallback) => Number.isInteger(value) && value >= 0 ? value : fallback

export function jobListPath({ scope = 'mine', limit = 50, offset = 0, status = '', keyword = '' } = {}) {
  const query = new URLSearchParams({
    scope: scope === 'mine' ? 'mine' : 'team',
    limit: String(Math.min(200, positiveInteger(limit, 50))),
    offset: String(nonNegativeInteger(offset, 0)),
  })
  if (status) query.set('status', String(status))
  if (keyword) query.set('keyword', String(keyword))
  return `/api/v1/jobs?${query.toString()}`
}

export function visibleJobScopes(userRoles = []) {
  const roles = Array.isArray(userRoles) ? userRoles : []
  return roles.includes('TenantAdmin') || roles.includes('SuperAdmin')
    ? ['mine', 'team']
    : ['mine']
}

export function normalizeJobListPage(value) {
  return {
    items: Array.isArray(value?.items) ? value.items : [],
    total: nonNegativeInteger(value?.total, 0),
    limit: positiveInteger(value?.limit, 50),
    offset: nonNegativeInteger(value?.offset, 0),
  }
}
