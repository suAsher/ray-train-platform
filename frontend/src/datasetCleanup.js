export const cleanupNotice = '仅移除失败记录，保留审计，不删除原始数据、Parquet文件或训练任务，不释放存储。'

export function canManageDataset(dataset, roles = [], tenantId) {
  if (roles.includes('SuperAdmin')) return true
  return Boolean(tenantId) && roles.includes('TenantAdmin') && dataset?.visibility === 'TEAM' && dataset.ownerTenantId === tenantId
}

export function visibleDatasetVersions(versions, showFailed = false) {
  return versions.filter(version => showFailed || version.state !== 'FAILED')
}

export async function cleanupFailedVersions(versionIds, remove) {
  const ids = [...new Set(versionIds)]
  if (!ids.length || ids.length > 100) throw new Error('每次请选择 1 至 100 条失败记录')
  let succeeded = []
  let failed = []
  for (const id of ids) {
    try {
      const response = await remove(id)
      if (response?.deleted !== true) throw new Error('服务端未确认移除，请刷新后核对')
      succeeded = [...succeeded, id]
    } catch (error) {
      failed = [...failed, { id, reason: error?.message || '移除失败，请稍后重试' }]
    }
  }
  return { succeeded, failed }
}
