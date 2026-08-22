import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import * as dataSpaceActions from './dataSpaceActions.js'

const { canManageDataSpace, canUseDataSpaceForTraining } = dataSpaceActions

test('ready IDC data can be selected for training without portal browsing', () => {
  assert.equal(canUseDataSpaceForTraining({ provider: 'idc', browseEnabled: false, mountStatus: 'ready' }), true)
})

test('a pending data mount cannot be selected for training', () => {
  assert.equal(canUseDataSpaceForTraining({ provider: 'tos', browseEnabled: true, mountStatus: 'pending' }), false)
})

test('only responsible administrators may publish shared TOS data', () => {
  assert.equal(canManageDataSpace({ id: 'team-shared', provider: 'tos' }, { roles: ['TenantAdmin'] }), true)
  assert.equal(canManageDataSpace({ id: 'public', provider: 'tos' }, { roles: ['TenantAdmin'] }), false)
  assert.equal(canManageDataSpace({ id: 'public', provider: 'tos' }, { roles: ['SuperAdmin'] }), true)
})

test('authoritative canWrite overrides rolling-deployment role inference', () => {
  assert.equal(canManageDataSpace({ id: 'team-shared', provider: 'tos', readOnly: true, canWrite: true }, { roles: ['Engineer'] }), true)
  assert.equal(canManageDataSpace({ id: 'my-files', provider: 'tos', readOnly: false, canWrite: true }, { roles: [] }), true)
  assert.equal(canManageDataSpace({ id: 'team-shared', provider: 'tos', readOnly: true, canWrite: false }, { roles: ['SuperAdmin'] }), false)
  assert.equal(canManageDataSpace({ id: 'my-files', provider: 'tos', readOnly: false, canWrite: false }, { roles: ['SuperAdmin'] }), false)
})

test('rolling-deployment fallback requires a recognized platform role', () => {
  const personal = { id: 'my-files', provider: 'tos', readOnly: false }
  assert.equal(canManageDataSpace(personal, { roles: [] }), false)
  assert.equal(canManageDataSpace(personal, { roles: ['Viewer'] }), false)
  assert.equal(canManageDataSpace(personal, { roles: ['Engineer'] }), true)
  assert.equal(canManageDataSpace(personal, { roles: ['TenantAdmin'] }), true)
  assert.equal(canManageDataSpace(personal, { roles: ['SuperAdmin'] }), true)
})

test('access labels distinguish portal publishing from Pod mount semantics', () => {
  assert.equal(typeof dataSpaceActions.dataSpaceAccessLabel, 'function')
  assert.equal(typeof dataSpaceActions.dataSpaceAccessType, 'function')
  assert.equal(dataSpaceActions.dataSpaceAccessLabel({ id: 'team-shared', provider: 'tos', readOnly: true, canWrite: true }, { roles: ['TenantAdmin'] }), '管理员可发布 · Pod 只读')
  assert.equal(dataSpaceActions.dataSpaceAccessLabel({ id: 'my-files', provider: 'tos', readOnly: false, canWrite: true }, { roles: ['Engineer'] }), '可写')
  assert.equal(dataSpaceActions.dataSpaceAccessLabel({ id: 'team-shared', provider: 'tos', readOnly: true, canWrite: false }, { roles: ['Engineer'] }), '只读')
  assert.equal(dataSpaceActions.dataSpaceAccessType({ readOnly: true, canWrite: true }, { roles: [] }), 'warning')
  assert.equal(dataSpaceActions.dataSpaceAccessType({ readOnly: false, canWrite: true }, { roles: [] }), 'success')
  assert.equal(dataSpaceActions.dataSpaceAccessType({ readOnly: true, canWrite: false }, { roles: ['SuperAdmin'] }), 'info')
})

test('storage readiness gates mutations after authorization', () => {
  assert.equal(typeof dataSpaceActions.canMutateDataSpace, 'function')
  const writable = { id: 'my-files', provider: 'tos', readOnly: false, canWrite: true, storageStatus: 'ready', mountStatus: 'pending' }
  assert.equal(dataSpaceActions.canMutateDataSpace(writable, { roles: ['Engineer'] }), true)
  assert.equal(dataSpaceActions.canMutateDataSpace({ ...writable, storageStatus: 'not-configured' }, { roles: ['Engineer'] }), false)
  assert.equal(dataSpaceActions.canMutateDataSpace({ id: 'my-files', provider: 'tos', readOnly: false, canWrite: true, mountStatus: 'ready' }, { roles: [] }), true)
})

test('DataCache mutation buttons use the authorized storage-ready capability', () => {
  const view = readFileSync(new URL('./views/DataCache/index.vue', import.meta.url), 'utf8')
  assert.match(view, /v-if="canMutate"[^>]*@click="folderDialogVisible = true"/)
  assert.match(view, /v-if="canMutate"[^>]*@click="fileInput\?\.click\(\)"/)
  assert.match(view, /v-if="selectedSpace\.id === 'workspace' && canMutate"[^>]*@click="folderInput\?\.click\(\)"/)
  assert.match(view, /v-if="selectedSpace\.id === 'workspace' && canMutate"[^>]*@click="createSnapshot"/)
})
