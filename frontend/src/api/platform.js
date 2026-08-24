import { apiGet, apiPost } from './client'

/**
 * Deployment ceilings, governed mount paths and selectable execution
 * profiles. The Portal renders every GPU picker from this payload so it can
 * never offer a shape the server will reject.
 */
export function fetchPlatformLimits() {
  return apiGet('/api/v1/limits')
}

/**
 * Resolve a branch or tag to the commit a submission pins. Users no longer
 * have to copy a SHA out of GitLab, and the job still records an immutable
 * commit.
 */
export function resolveGitRef(repositoryUrl, ref) {
  return apiPost('/api/v1/git/resolve-ref', { repositoryUrl, ref })
}

/**
 * Live per-GPU state from DCGM Exporter via Prometheus: utilization, memory,
 * temperature and power. The devices page previously showed only Kubernetes
 * GPU requests, which says what is reserved but not what is actually busy.
 */
export function fetchGPUMetrics() {
  return apiGet('/api/v1/cluster/gpu-metrics')
}

export function fetchGPUHistory(window, node = '') {
  const query = new URLSearchParams({ window })
  if (node) query.set('node', node)
  return apiGet(`/api/v1/cluster/gpu-metrics/history?${query.toString()}`)
}
