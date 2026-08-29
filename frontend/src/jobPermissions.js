const ACTIVE_JOB_STATES = new Set([
  'SUBMITTED',
  'VALIDATING',
  'QUEUED',
  'ADMITTED',
  'PROVISIONING',
  'RUNNING',
  'RECOVERING',
  'CANCELING',
])

export function canCancelJob(job, session = {}) {
  if (!job || !ACTIVE_JOB_STATES.has(String(job.status || '').toUpperCase())) return false

  const roles = new Set((session.roles || []).map((role) => String(role).toLowerCase()))
  if (roles.has('superadmin') || roles.has('tenantadmin')) return true

  return Boolean(session.userId) && job.userId === session.userId
}
