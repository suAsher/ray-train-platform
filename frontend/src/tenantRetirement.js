export function visibleTenants(tenants = [], includeRetired = false, isSuperAdmin = false) {
  return tenants.filter((tenant) => !tenant.retiredAt || (includeRetired && isSuperAdmin))
}

export function canConfirmRetirement({ isSuperAdmin, tenant, preflight, confirmation, busy }) {
  return Boolean(isSuperAdmin && !busy && tenant?.id && !tenant.retiredAt
    && preflight?.tenantId === tenant.id && preflight.canRetire === true && !preflight.retiredAt
    && Array.isArray(preflight.blockers) && preflight.blockers.length === 0
    && confirmation === tenant.id)
}

const countLabels = {
	 historicalJobs: '历史训练（保留）', sessions: '会话记录', tokens: '令牌记录',
	 datasets: '团队数据集（保留）', images: '团队镜像登记（保留）',
	 mounts: '挂载引用（保留）', storageAssets: '存储资产引用（保留）',
  members: '成员记录', jobs: '活动训练任务', workspaces: '活动工作区',
  publications: '活动发布任务', uploads: '活动上传', transfers: '活动传输',
}

export function retirementCountRows(counts = {}) {
  return Object.entries(counts).map(([key, value]) => ({ key, label: countLabels[key] || key, value }))
}

const blockerLabels = {
  active_jobs: '仍有活动训练任务，请等待任务结束后重新检查',
  active_workspaces: '仍有活动工作区，请停止工作区后重新检查',
  active_publications: '仍有活动发布任务，可能包含公共发布；为避免影响发布，暂不能退役，请等待发布结束后重新检查',
  active_uploads: '仍有活动上传，请等待上传结束后重新检查',
  active_transfers: '仍有活动传输，请等待传输结束后重新检查',
  writes_in_flight: '仍有写入请求正在处理，请稍后重新检查',
  k8s_state_unknown: '无法确认集群资源状态，请稍后重新检查',
  protected_tenant: '系统初始团队受保护，不能退役',
  current_tenant: '不能退役当前登录所属团队',
  already_retired: '该团队已退役',
}

export function retirementBlockerText(blocker) {
  const text = typeof blocker === 'string' ? blocker : blocker.message || blocker.code
  return blockerLabels[text] || text
}
