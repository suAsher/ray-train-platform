import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import { computed, ref, watch } from 'vue'
import * as retirement from './tenantRetirement.js'
import { adminQuotaModel, defaultPlatformLimits } from './platformLimits.js'

// Compile the real component's setup to exercise its async API orchestration.
const source = readFileSync(new URL('./components/admin/TenantPanel.vue', import.meta.url), 'utf8')
const script = compileScript(parse(source).descriptor, { id: 'tenant-retirement-test' }).content
  .replace(/^import .* from .*$/gm, '').replace('export default', 'return')
const team = { id: 'team-a', gpuQuotaLimit: 8 }
const ready = { tenantId: team.id, canRetire: true, blockers: [], counts: { members: 2 } }

function panel(overrides = {}, isSuperAdmin = true) {
  const calls = []
  const dependencies = {
    computed, ref, watch, ...retirement, adminQuotaModel, defaultPlatformLimits,
    ElMessage: { error: (message) => calls.push(['error', message]), success: () => {} },
    fetchTenantRetirementPreflight: async () => ready,
    fetchTenantsForRetirementAudit: async () => [team, { id: 'old', retiredAt: 'now' }],
    retireTenant: async (...args) => calls.push(['retire', ...args]),
    setTenantGPUQuota: async (...args) => calls.push(['quota', ...args]),
    ...overrides,
  }
  const component = new Function(...Object.keys(dependencies), script)(...Object.values(dependencies))
  const state = component.setup({ tenants: [team], isSuperAdmin, limits: defaultPlatformLimits }, {
    expose: () => {}, emit: (...args) => calls.push(args),
  })
  return { state, calls }
}

test('retirement checks first, requires exact ID and hides the retired team immediately after success', async () => {
  const { state, calls } = panel()
  await state.openRetirement(team)
  await state.submitRetirement()
  assert.deepEqual(calls, [])
  state.confirmation.value = team.id
  await state.submitRetirement()
  assert.deepEqual(calls, [['retire', team.id, team.id], ['changed']])
  assert.equal(state.retirementVisible.value, false)
  assert.deepEqual(state.displayedTenants.value, [])
})

test('failed retirement preserves confirmation and resource counts and requires a fresh check', async () => {
  const { state } = panel({ retireTenant: async () => { throw new Error('资源状态已变化') } })
  await state.openRetirement(team)
  state.confirmation.value = team.id
  await state.submitRetirement()
  assert.equal(state.retirementVisible.value, true)
  assert.equal(state.confirmation.value, team.id)
  assert.deepEqual(state.preflight.value.counts, { members: 2 })
  assert.equal(state.canRetire.value, false)
  assert.match(state.retirementError.value, /资源状态已变化/)
  await state.checkRetirement()
  assert.equal(state.canRetire.value, true)
})

test('failed refresh preserves the earlier report while preventing confirmation', async () => {
  let checks = 0
  const { state } = panel({ fetchTenantRetirementPreflight: async () => {
    if (checks++ === 0) return ready
    throw new Error('网络错误')
  } })
  await state.openRetirement(team)
  state.confirmation.value = team.id
  await state.checkRetirement()
  assert.deepEqual(state.preflight.value.counts, { members: 2 })
  assert.equal(state.canRetire.value, false)
})

test('pending checks prevent quota editing and duplicate retirement requests', async () => {
  let finish
  const { state, calls } = panel({ fetchTenantRetirementPreflight: () => new Promise((resolve) => { finish = resolve }) })
  const request = state.openRetirement(team)
  state.openQuota(team)
  state.confirmation.value = team.id
  await state.submitRetirement()
  assert.equal(state.quotaVisible.value, false)
  assert.deepEqual(calls, [])
  finish(ready)
  await request
})

test('audit toggle shows retired teams, and retired quota edits and non-admin actions are rejected', async () => {
  const { state } = panel()
  await state.toggleRetired(true)
  assert.equal(state.displayedTenants.value.length, 2)
  state.openQuota(state.displayedTenants.value[1])
  assert.equal(state.quotaVisible.value, false)
  await state.toggleRetired(false)
  assert.deepEqual(state.displayedTenants.value, [team])
  const other = panel({}, false)
  await other.state.openRetirement(team)
  await other.state.toggleRetired(true)
  other.state.openQuota(team)
  assert.equal(other.state.retirementVisible.value, false)
  assert.equal(other.state.includeRetired.value, false)
  assert.equal(other.state.quotaVisible.value, false)
})

test('retirement API methods encode tenant IDs and send the typed confirmation', async () => {
  const calls = []
  const apiSource = readFileSync(new URL('./api/catalog.js', import.meta.url), 'utf8')
    .replace(/^import .* from .*$/gm, '').replaceAll('export function', 'function')
  const api = new Function('apiGet', 'apiPost', `${apiSource}; return { fetchTenantsForRetirementAudit, fetchTenantRetirementPreflight, retireTenant }`)(
    async (...args) => calls.push(['GET', ...args]), async (...args) => calls.push(['POST', ...args]),
  )
  await api.fetchTenantsForRetirementAudit()
  await api.fetchTenantRetirementPreflight('team/with space')
  await api.retireTenant('team/with space', 'team/with space')
  assert.deepEqual(calls, [
    ['GET', '/api/v1/tenants?includeRetired=true'],
    ['GET', '/api/v1/tenants/team%2Fwith%20space/retirement-preflight'],
    ['POST', '/api/v1/tenants/team%2Fwith%20space/retire', { confirmTenantId: 'team/with space' }],
  ])
})
