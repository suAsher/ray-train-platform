import assert from 'node:assert/strict'
import test from 'node:test'
import { canConfirmRetirement, visibleTenants, retirementCountRows, retirementBlockerText } from './tenantRetirement.js'

test('retired teams are hidden by default and auditing is restricted to superadmins', () => {
  const teams = [{ id: 'active' }, { id: 'retired', retiredAt: '2026-09-06T00:00:00Z' }]
  assert.deepEqual(visibleTenants(teams), [teams[0]])
  assert.deepEqual(visibleTenants(teams, true, false), [teams[0]])
  assert.deepEqual(visibleTenants(teams, true, true), teams)
  assert.deepEqual(visibleTenants(), [])
})

test('retirement requires an exact confirmation, matching preflight, permission and no blockers', () => {
  const state = { isSuperAdmin: true, tenant: { id: 'team-a' }, preflight: { tenantId: 'team-a', canRetire: true, blockers: [] }, confirmation: 'team-a', busy: false }
  assert.equal(canConfirmRetirement(state), true)
  for (const patch of [
    { isSuperAdmin: false }, { busy: true }, { tenant: null }, { preflight: null },
    { confirmation: ' team-a' }, { confirmation: 'TEAM-A' },
    { tenant: { id: 'team-a', retiredAt: 'now' } },
    { preflight: { tenantId: 'other', canRetire: true, blockers: [] } },
    { preflight: { tenantId: 'team-a', canRetire: false, blockers: [] } },
    { preflight: { tenantId: 'team-a', canRetire: true, blockers: ['running'] } },
    { preflight: { tenantId: 'team-a', canRetire: true } },
    { preflight: { tenantId: 'team-a', canRetire: true, blockers: [], retiredAt: 'now' } },
  ]) assert.equal(canConfirmRetirement({ ...state, ...patch }), false)
})

test('counts and blockers retain server information including unfamiliar resources', () => {
  assert.deepEqual(retirementCountRows({ members: 2, futureResource: 3 }), [
    { key: 'members', label: '成员记录', value: 2 }, { key: 'futureResource', label: 'futureResource', value: 3 },
  ])
  assert.deepEqual(retirementCountRows(), [])
  assert.equal(retirementBlockerText('存在运行任务'), '存在运行任务')
  assert.equal(retirementBlockerText({ code: 'busy', message: '仍在运行' }), '仍在运行')
  assert.equal(retirementBlockerText({ code: 'busy' }), 'busy')
})

test('active-resource blockers explain the required wait and conservative public publication check', () => {
  const expected = {
    active_jobs: '仍有活动训练任务，请等待任务结束后重新检查',
    active_workspaces: '仍有活动工作区，请停止工作区后重新检查',
    active_publications: '仍有活动发布任务，可能包含公共发布；为避免影响发布，暂不能退役，请等待发布结束后重新检查',
    active_uploads: '仍有活动上传，请等待上传结束后重新检查',
    active_transfers: '仍有活动传输，请等待传输结束后重新检查',
    writes_in_flight: '仍有写入请求正在处理，请稍后重新检查',
  }
  for (const [code, message] of Object.entries(expected)) {
    assert.equal(retirementBlockerText(code), message)
    assert.equal(retirementBlockerText({ code }), message)
  }
  assert.equal(retirementBlockerText('future_resource_busy'), 'future_resource_busy')
})
