const activeStates = ['SUBMITTED', 'VALIDATING', 'QUEUED', 'ADMITTED', 'PROVISIONING', 'RUNNING', 'RECOVERING']

async function loadState(apiGet, state) {
  let offset = 0
  let jobs = []
  for (let pageNumber = 0; pageNumber < 1000; pageNumber += 1) {
    // Authorization remains server-side: SuperAdmin sees all teams and
    // TenantAdmin only their own. Omitting scope defaults to the current user.
    const query = new URLSearchParams({ scope: 'team', status: state, limit: '200', offset: String(offset) })
    const page = await apiGet(`/api/v1/jobs?${query}`)
    if (!Array.isArray(page?.items) || !Number.isSafeInteger(page.total) || page.total < 0) {
      throw new Error('训练任务列表响应不完整，请刷新重试')
    }
    if (page.items.length === 0 && offset < page.total) {
      throw new Error('训练任务分页结果不完整，请刷新重试')
    }
    jobs = [...jobs, ...page.items]
    offset += page.items.length
    if (offset >= page.total) return jobs
  }
  throw new Error('训练任务超过分页上限或持续变化，请刷新重试')
}

export async function loadAdminActiveJobs(apiGet) {
  const pages = await Promise.all(activeStates.map((state) => loadState(apiGet, state)))
  const rowsByID = new Map()
  for (const job of pages.flat()) {
    const resources = job.spec?.resources || {}
    rowsByID.set(job.id, {
      id: job.id,
      name: job.spec?.name || job.id,
      tenantId: job.tenantId,
      state: job.observedState,
      gpus: (resources.workerReplicas || 0) * (resources.gpusPerWorker || 0),
      createdAt: job.createdAt ? new Date(job.createdAt).toLocaleString('zh-CN', { hour12: false }) : '',
    })
  }
  return [...rowsByID.values()]
}

export async function refreshAdminActiveJobs(apiGet, previous = { jobs: [], available: false, error: '' }) {
  try {
    return { jobs: await loadAdminActiveJobs(apiGet), available: true, error: '' }
  } catch (error) {
    return { ...previous, error: error?.message || '无法读取训练任务列表' }
  }
}
