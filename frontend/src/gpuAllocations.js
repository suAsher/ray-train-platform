const TYPES = new Set(['TRAINING_JOB', 'DEBUG_WORKSPACE'])

const safeText = (value) => String(value || '').trim()

export function normalizeGPUAllocations(payload) {
  const items = Array.isArray(payload) ? payload : Array.isArray(payload?.items) ? payload.items : []
  return items.map((item) => ({
    id: safeText(item?.id),
    type: TYPES.has(item?.type) ? item.type : safeText(item?.type),
    name: safeText(item?.name) || safeText(item?.id),
    tenantId: safeText(item?.tenantId),
    userId: safeText(item?.userId),
    username: safeText(item?.username) || safeText(item?.userId),
    state: safeText(item?.state),
    gpuCount: Number.isFinite(Number(item?.gpuCount)) ? Math.max(0, Math.trunc(Number(item.gpuCount))) : 0,
    namespace: safeText(item?.namespace),
    resourceName: safeText(item?.resourceName),
    createdAt: safeText(item?.createdAt),
    startedAt: safeText(item?.startedAt),
  }))
}

export function allocationSummary(items = []) {
  return items.reduce((summary, item) => ({
    trainingJobs: summary.trainingJobs + (item.type === 'TRAINING_JOB' ? 1 : 0),
    debugWorkspaces: summary.debugWorkspaces + (item.type === 'DEBUG_WORKSPACE' ? 1 : 0),
    detailedGPUs: summary.detailedGPUs + Math.max(0, Number(item.gpuCount) || 0),
  }), { trainingJobs: 0, debugWorkspaces: 0, detailedGPUs: 0 })
}

export function formatAllocationDuration(item, now = new Date()) {
  const start = new Date(item?.startedAt || item?.createdAt || '')
  const end = now instanceof Date ? now : new Date(now)
  if (!Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || end < start) return '—'
  const totalMinutes = Math.max(1, Math.floor((end.getTime() - start.getTime()) / 60000))
  const days = Math.floor(totalMinutes / 1440)
  const hours = Math.floor((totalMinutes % 1440) / 60)
  const minutes = totalMinutes % 60
  if (days) return `${days}天${hours}小时`
  if (hours) return `${hours}小时${minutes}分钟`
  return `${minutes}分钟`
}

export const allocationTypeLabel = (type) => type === 'DEBUG_WORKSPACE' ? '交互式调试' : '训练任务'

export const allocationTypeTag = (type) => type === 'DEBUG_WORKSPACE' ? 'warning' : 'primary'

export const allocationStateTag = (state) => {
  if (state === 'RUNNING' || state === 'RECOVERING') return 'success'
  if (['CANCELING', 'DELETING', 'STOPPING'].includes(state)) return 'danger'
  return 'warning'
}

export const formatAllocationTime = (value) => {
  const date = new Date(value || '')
  return Number.isFinite(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false }) : '—'
}
