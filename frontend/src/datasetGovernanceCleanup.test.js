import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import { computed, ref } from 'vue'
import * as catalog from './datasetCatalog.js'
import { cleanupFailedVersions } from './datasetCleanup.js'

const source = readFileSync(new URL('./components/admin/DatasetGovernancePanel.vue', import.meta.url), 'utf8')
const script = compileScript(parse(source).descriptor, { id: 'governance-cleanup' }).content
  .replace(/^import[\s\S]*?from ['"][^'"]+['"];?$/gm, '').replace('export default', 'return')
const dataset = { id: 'd', name: '测试', visibility: 'PUBLIC' }
const versions = [{ id: 'a', state: 'FAILED' }, { id: 'b', state: 'FAILED' }, { id: 'r', state: 'READY' }]
function panel(overrides = {}, roleList = ['SuperAdmin']) {
  const calls = []
  const dependencies = {
    computed, ref, watch: () => {}, ...catalog, cleanupFailedVersions,
    roles: ref(roleList), session: ref({ tenantId: 'own' }),
    ElMessage: Object.fromEntries(['error', 'warning', 'success'].map(kind => [kind, message => calls.push([kind, message])])),
    ElMessageBox: { confirm: async (...args) => calls.push(['confirm', ...args]) },
    createDataset: () => {}, deprecateDatasetVersion: () => {}, fetchDatasets: async () => [],
    fetchDatasetPublication: async () => null, fetchDatasetVersions: async () => versions,
    previewDatasetGarbageCollection: () => {}, requestDatasetPublication: async () => calls.push(['publish']),
    purgeFailedDatasetVersion: async (...args) => { calls.push(['purge', ...args]); return { deleted: true, exclusiveObjectsDeleted: 0 } },
    ...overrides,
  }
  const component = new Function(...Object.keys(dependencies), script)(...Object.values(dependencies))
  const state = component.setup({ capabilities: { versioningEnabled: true, publisherEnabled: true }, isSuperAdmin: true }, { expose: () => {} })
  state.versionsByDataset.value = { d: versions }
  return { state, calls }
}

test('governance exposes single and batch irreversible cleanup only for failed versions', async () => {
  assert.match(source, /批量删除失败版本/)
  assert.match(source, /删除失败版本/)
  const { state, calls } = panel()
  await state.purgeVersions(dataset, ['a', 'r'])
  assert.deepEqual(calls, [])
  await state.purgeVersions(dataset, ['a'])
  assert.deepEqual(calls.filter(c => c[0] === 'purge'), [['purge', 'd', 'a']])
  assert.match(calls[0][1], /不可恢复/)
  assert.match(calls[0][1], /原始数据.*共享.*训练/)
  assert.deepEqual(state.versionsFor('d').map(v => v.id), ['b', 'r'])
})

test('batch preserves failed requests and reports partial completion without storage claims', async () => {
  const { state, calls } = panel({ purgeFailedDatasetVersion: async (_, id) => {
    if (id === 'b') throw Object.assign(new Error('unsafe internals'), { code: 'DATASET_VERSION_REFERENCED' })
    return { deleted: true }
  } })
  await state.purgeVersions(dataset, ['a', 'b'])
  assert.deepEqual(state.versionsFor('d').map(v => v.id), ['b', 'r'])
  assert.equal(state.cleanupErrors.value.d[0].id, 'b')
  assert.doesNotMatch(state.cleanupErrors.value.d[0].reason, /unsafe internals/)
  assert.doesNotMatch(JSON.stringify(calls), /已释放/)
})

test('cleanup holds a lock through confirmation and blocks publish, duplicates, stale permissions', async () => {
  let confirm
  const { state, calls } = panel({ ElMessageBox: { confirm: () => new Promise(resolve => { confirm = resolve }) } })
  const pending = state.purgeVersions(dataset, ['a'])
  assert.equal(state.cleanupBusy.value, true)
  await state.purgeVersions(dataset, ['b'])
  await state.publishDataset(dataset)
  assert.deepEqual(calls, [])
  confirm()
  await pending
  assert.equal(state.cleanupBusy.value, false)
})

test('tenant admin may clean only own TEAM datasets, never public or another team', async () => {
  const { state, calls } = panel({}, ['TenantAdmin'])
  await state.purgeVersions(dataset, ['a'])
  await state.purgeVersions({ ...dataset, visibility: 'TEAM', ownerTenantId: 'other' }, ['a'])
  assert.deepEqual(calls, [])
  await state.purgeVersions({ ...dataset, visibility: 'TEAM', ownerTenantId: 'own' }, ['a'])
  assert.equal(calls.filter(c => c[0] === 'purge').length, 1)
})

test('confirmation cancellation and permission loss never issue destructive requests', async () => {
  const cancelled = panel({ ElMessageBox: { confirm: async () => { throw 'cancel' } } })
  await cancelled.state.purgeVersions(dataset, ['a'])
  assert.deepEqual(cancelled.calls, [])
  assert.equal(cancelled.state.cleanupBusy.value, false)
  const currentRoles = ref(['SuperAdmin'])
  const changed = panel({ roles: currentRoles, ElMessageBox: { confirm: async () => { currentRoles.value = [] } } })
  await changed.state.purgeVersions(dataset, ['a'])
  assert.deepEqual(changed.calls, [])
  assert.equal(changed.state.cleanupBusy.value, false)
})

test('backend refusal without a deletion receipt leaves the row visible', async () => {
  const { state } = panel({ purgeFailedDatasetVersion: async () => ({ deleted: false }) })
  await state.purgeVersions(dataset, ['a'])
  assert.deepEqual(state.versionsFor('d').map(v => v.id), ['a', 'b', 'r'])
  assert.equal(state.cleanupErrors.value.d.length, 1)
})

test('existing dataset view offers permanent cleanup separately from record-only removal', () => {
  const page = readFileSync(new URL('./views/Datasets/index.vue', import.meta.url), 'utf8')
  assert.match(page, /永久删除失败版本及独占产物/)
  assert.match(page, /purgeFailedDatasetVersion/)
  assert.match(page, /deleteFailedDatasetVersion/)
})

test('purge API encodes identifiers and uses a separate destructive endpoint', async () => {
  const apiSource = readFileSync(new URL('./api/datasets.js', import.meta.url), 'utf8')
    .replace(/^import .* from .*$/gm, '').replace(/^export \{.*\} from .*$/gm, '').replaceAll('export function', 'function')
  const { datasetVersionPath } = await import('./api/datasetPaths.js')
  const calls = []
  const purge = new Function('apiDelete', 'datasetVersionPath', `${apiSource}; return purgeFailedDatasetVersion`)(async path => calls.push(path), datasetVersionPath)
  await purge('a/b', 'c+d')
  assert.deepEqual(calls, ['/api/v1/datasets/a%2Fb/versions/c%2Bd/purge'])
})
