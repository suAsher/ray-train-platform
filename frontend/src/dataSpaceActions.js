import { dataSpaceReadiness, dataSpaceStorageReady } from './dataSpaceReadiness.js'

// Browser enumeration is an optional convenience. IDC NFS roots deliberately
// remain non-browsable in the Portal but, once their mount is ready, are valid
// training inputs exactly like a ready TOS data space.
export function canUseDataSpaceForTraining(space) {
  return Boolean(space) && dataSpaceReadiness(space).ready
}

export function canManageDataSpace(space, session) {
  if (!space) return false
  if (typeof space.canWrite === 'boolean') return space.canWrite
  if (space.provider !== 'tos') return false
  const roles = new Set((session?.roles || []).map((role) => String(role).toLowerCase()))
  const engineer = roles.has('engineer')
  const tenantAdmin = roles.has('tenantadmin')
  const superAdmin = roles.has('superadmin')
  if (!engineer && !tenantAdmin && !superAdmin) return false
  if (space.id === 'team-shared') return tenantAdmin || superAdmin
  if (space.id === 'public') return superAdmin
  return !space.readOnly
}

export function dataSpaceAccessLabel(space, session) {
  if (!canManageDataSpace(space, session)) return '只读'
  return space?.readOnly ? '管理员可发布 · Pod 只读' : '可写'
}

export function dataSpaceAccessType(space, session) {
  if (!canManageDataSpace(space, session)) return 'info'
  return space?.readOnly ? 'warning' : 'success'
}

export function canMutateDataSpace(space, session) {
  return canManageDataSpace(space, session) && dataSpaceStorageReady(space)
}

export function dataPageSpaces(spaces) {
  const catalog = Array.isArray(spaces) ? [...spaces] : []
  if (!catalog.some((space) => space?.id === 'my-storage')) return catalog
  return catalog.filter((space) => space?.id !== 'my-files' && space?.id !== 'my-runs')
}

export function trainingInputDataSpaces(spaces) {
  const catalog = Array.isArray(spaces) ? [...spaces] : []
  const hasPersonalRoot = catalog.some((space) => space?.id === 'my-storage')
  return catalog.filter((space) => {
    if (space?.id === 'workspace' || space?.id === 'my-runs') return false
    return !(hasPersonalRoot && space?.id === 'my-files')
  })
}
