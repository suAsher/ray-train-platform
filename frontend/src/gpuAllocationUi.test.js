import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const [controlCenter, quotaManage, queuePanel, allocationTable] = await Promise.all([
  readFile(new URL('./views/ControlCenter/index.vue', import.meta.url), 'utf8'),
  readFile(new URL('./views/QuotaManage/index.vue', import.meta.url), 'utf8'),
  readFile(new URL('./components/admin/QueuePanel.vue', import.meta.url), 'utf8'),
  readFile(new URL('./components/admin/GPUAllocationTable.vue', import.meta.url), 'utf8'),
])

test('compute overview loads one role-scoped allocation endpoint', () => {
  assert.match(controlCenter, /\/api\/v1\/gpu-allocations/)
  assert.match(controlCenter, /GPU 占用明细/)
  assert.doesNotMatch(controlCenter, /\/api\/v1\/jobs\?status=RUNNING/)
  assert.match(controlCenter, /allocationsLoaded/)
  assert.match(controlCenter, /allocationError/)
  assert.match(controlCenter, /明细占用 GPU/)
  assert.match(controlCenter, /训练任务/)
  assert.match(controlCenter, /调试环境/)
  assert.doesNotMatch(controlCenter, /allocationError[\s\S]{0,500}allocations\.value = \[\]/)
})

test('admin queue keeps training controls and receives debug allocations', () => {
  assert.match(quotaManage, /loadGPUAllocations/)
  assert.match(quotaManage, /:allocations="gpuAllocations"/)
  assert.match(queuePanel, /交互式调试环境/)
  assert.match(queuePanel, /GPUAllocationTable/)
  assert.match(queuePanel, /queueJobAction/)
  assert.match(queuePanel, /allocationAvailable/)
  assert.match(queuePanel, /占用明细暂不可用/)
  assert.match(quotaManage, /gpuAllocationsLoaded/)
  assert.doesNotMatch(quotaManage, /catch \(error\)[\s\S]{0,120}gpuAllocations\.value = \[\]/)
})

test('allocation table identifies owner, team, type, GPU, duration and resource', () => {
  for (const label of ['类型', '用户', '团队', 'GPU', '运行时长', '资源位置']) {
    assert.match(allocationTable, new RegExp(label))
  }
  assert.match(allocationTable, /username/)
  assert.match(allocationTable, /tenantId/)
  assert.match(allocationTable, /namespace/)
  assert.match(allocationTable, /resourceName/)
})
