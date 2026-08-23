/** Return the only action the current tenant may perform on an active workload. */
export function queueJobAction(job, currentTenantId, isSuperAdmin = false) {
  const callerTenant = String(currentTenantId || '').trim()
  const jobTenant = String(job?.tenantId || '').trim()
  if (!isSuperAdmin && (!callerTenant || jobTenant !== callerTenant)) return null
  if (job?.state === 'QUEUED') return { kind: 'cancel-queue', label: '取消排队' }
  if (job?.state === 'RUNNING' || job?.state === 'PROVISIONING') return { kind: 'stop', label: '停止任务' }
  return null
}

export function queuePanelStats(jobs, clusterGPUs, physicalAllocatedGPUs) {
  const rows = Array.isArray(jobs) ? jobs : []
  const running = rows.filter((job) => job.state === 'RUNNING')
  const provisioning = rows.filter((job) => ['SUBMITTED', 'VALIDATING', 'ADMITTED', 'PROVISIONING'].includes(job.state))
  const queued = rows.filter((job) => job.state === 'QUEUED')
  const sumGPUs = (items) => items.reduce((total, job) => total + Number(job.gpus || 0), 0)
  const activeRequestedGPUs = sumGPUs([...running, ...provisioning])
  const allocated = Number(physicalAllocatedGPUs || 0)
  return {
    runningJobs: running.length,
    waitingJobs: provisioning.length + queued.length,
    activeRequestedGPUs,
    queuedRequestedGPUs: sumGPUs(queued),
    physicalAllocatedGPUs: allocated,
    releasingGPUs: Math.max(allocated - activeRequestedGPUs, 0),
    clusterGPUs: Number(clusterGPUs || 0),
  }
}
