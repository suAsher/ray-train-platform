function streamRole(pod) {
  const normalized = String(pod || '').toLowerCase()
  if (normalized.includes('-raycluster-') && normalized.includes('-head-')) return 'head'
  if (normalized.includes('-raycluster-') && normalized.includes('worker')) return 'worker'
  return 'submitter'
}

export function buildLogStreamCards(pods, totalGpus = 0) {
  const uniquePods = [...new Set((pods || []).filter(Boolean))]
  const workers = uniquePods.filter((pod) => streamRole(pod) === 'worker').sort()
  const workerIndexes = new Map(workers.map((pod, index) => [pod, index + 1]))
  const order = { submitter: 0, head: 1, worker: 2 }
  const sorted = [...uniquePods].sort((left, right) => {
    const roleDifference = order[streamRole(left)] - order[streamRole(right)]
    return roleDifference || left.localeCompare(right)
  })
  const cards = sorted.map((pod) => {
    const role = streamRole(pod)
    if (role === 'head') {
      return { id: pod, pod, role, label: 'Ray Head', sub: `集群控制面、调度与 Dashboard · ${pod}`, active: true }
    }
    if (role === 'worker') {
      return { id: pod, pod, role, label: `训练 Worker ${workerIndexes.get(pod)}`, sub: `GPU 用户训练代码与分布式进程输出 · ${pod}`, active: true }
    }
    return { id: pod, pod, role, label: '任务提交器', sub: `创建 RayJob、上传代码并汇报最终状态 · ${pod}`, active: true }
  })
  return [{
    id: 'ALL', role: 'all', label: '全部训练日志',
    sub: `${uniquePods.length} 个日志流 · ${workers.length} 个训练 Worker · ${Number(totalGpus) || 0} 张 GPU`, active: true,
  }, ...cards]
}
