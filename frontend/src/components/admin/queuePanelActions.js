/** Return the only action the current tenant may perform on an active workload. */
export function queueJobAction(job, currentTenantId, isSuperAdmin = false) {
  const callerTenant = String(currentTenantId || '').trim()
  const jobTenant = String(job?.tenantId || '').trim()
  if (!isSuperAdmin && (!callerTenant || jobTenant !== callerTenant)) return null
  if (job?.state === 'QUEUED') return { kind: 'cancel-queue', label: '取消排队' }
  if (['RUNNING', 'RECOVERING', 'PROVISIONING'].includes(job?.state)) return { kind: 'stop', label: '停止任务' }
  return null
}

export function queuePanelStats(jobs, clusterGPUs, physicalAllocatedGPUs) {
  const rows = Array.isArray(jobs) ? jobs : []
  const keyed = new Map()
  const unkeyed = []
  for (const job of rows) {
    const id = String(job?.id || '').trim()
    if (id) keyed.set(id, job)
    else unkeyed.push(job)
  }
  const uniqueRows = [...keyed.values(), ...unkeyed]
  const running = uniqueRows.filter((job) => ['RUNNING', 'RECOVERING'].includes(job.state))
  const provisioning = uniqueRows.filter((job) => ['SUBMITTED', 'VALIDATING', 'ADMITTED', 'PROVISIONING'].includes(job.state))
  const queued = uniqueRows.filter((job) => job.state === 'QUEUED')
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
