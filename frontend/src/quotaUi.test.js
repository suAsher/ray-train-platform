import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const [createJobSource, quotaManageSource, tenantPanelSource, queuePanelSource] = await Promise.all([
  readFile(new URL('./views/Job/CreateJob.vue', import.meta.url), 'utf8'),
  readFile(new URL('./views/QuotaManage/index.vue', import.meta.url), 'utf8'),
  readFile(new URL('./components/admin/TenantPanel.vue', import.meta.url), 'utf8'),
  readFile(new URL('./components/admin/QueuePanel.vue', import.meta.url), 'utf8'),
])

test('job create view renders authoritative team quota values and role-aware limit label', () => {
  assert.match(createJobSource, /quotaModel\.scopeLabel/)
  assert.match(createJobSource, /quotaModel\.gpuLimit/)
  assert.match(createJobSource, /quotaModel\.gpuUsed/)
  assert.match(createJobSource, /quotaModel\.gpuAvailable/)
  assert.match(createJobSource, /quotaModel\.blockMessage/)
  assert.match(createJobSource, />管理员分配额度</)
  assert.match(createJobSource, />当前可提交上限</)
  assert.doesNotMatch(createJobSource, />团队配额<|>当前可用</)
  assert.doesNotMatch(createJobSource, /集群上限/)
})

test('quota management views use role-aware copy while quota editing stays SuperAdmin-only', () => {
  assert.match(quotaManageSource, /adminQuotaModel/)
  assert.match(quotaManageSource, /quotaCopy\.pageSummary/)
  assert.match(tenantPanelSource, /copy\.title/)
  assert.match(tenantPanelSource, /copy\.panelSummary/)
  assert.match(tenantPanelSource, />管理员分配额度</)
  assert.match(tenantPanelSource, />已使用</)
  assert.match(tenantPanelSource, /v-if="isSuperAdmin"[^>]*>修改配额/)
  assert.doesNotMatch(`${quotaManageSource}\n${tenantPanelSource}`, /有效配额|当前可用|集群当前可分配/)
})

test('quota UI contains no fixed 16 or 24 GPU fleet assumptions', () => {
  for (const source of [createJobSource, quotaManageSource, tenantPanelSource]) {
    assert.doesNotMatch(source, /(?:16|24)\s*(?:张|卡|GPU)/)
  }
})

test('queue actions are tenant-scoped and TenantAdmin copy does not claim quota allocation', () => {
  assert.match(quotaManageSource, /:current-tenant-id="currentTenantId"/)
  assert.match(quotaManageSource, /queueJobAction\(job, currentTenantId\.value\)/)
  assert.match(quotaManageSource, /TenantAdmin（管理本团队成员与团队共享目录）/)
  assert.doesNotMatch(quotaManageSource, /TenantAdmin（管理本团队配额与成员）/)
  assert.match(queuePanelSource, /currentTenantId/)
  assert.match(queuePanelSource, /queueJobAction/)
  assert.match(queuePanelSource, /v-if="actionFor\(scope\.row\)"/)
})
