/** Return the only action the current tenant may perform on an active workload. */
export function queueJobAction(job, currentTenantId) {
  const callerTenant = String(currentTenantId || '').trim()
  const jobTenant = String(job?.tenantId || '').trim()
  if (!callerTenant || jobTenant !== callerTenant) return null
  if (job?.state === 'QUEUED') return { kind: 'cancel-queue', label: '取消排队' }
  if (job?.state === 'RUNNING' || job?.state === 'PROVISIONING') return { kind: 'stop', label: '停止任务' }
  return null
}
