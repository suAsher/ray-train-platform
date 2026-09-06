import test from 'node:test'
import assert from 'node:assert/strict'
import { canManageDataset, visibleDatasetVersions, cleanupFailedVersions, cleanupNotice } from './datasetCleanup.js'

test('cleanup privileges require SuperAdmin or matching TEAM tenant admin', () => {
  const dataset = { visibility: 'TEAM', ownerTenantId: 'a' }
  assert.equal(canManageDataset(dataset, ['SuperAdmin'], 'b'), true)
  assert.equal(canManageDataset(dataset, ['TenantAdmin'], 'a'), true)
  assert.equal(canManageDataset(dataset, ['TenantAdmin'], 'b'), false)
  assert.equal(canManageDataset({ ...dataset, visibility: 'PUBLIC' }, ['TenantAdmin'], 'a'), false)
  assert.equal(canManageDataset(dataset, ['User'], 'a'), false)
  assert.equal(canManageDataset({ visibility: 'TEAM' }, ['TenantAdmin']), false)
})

test('failed versions are hidden by default without hiding active or ready versions', () => {
  const versions = [{ id: 'f', state: 'FAILED' }, { id: 'r', state: 'READY' }, { id: 'p', state: 'PACKING' }]
  assert.deepEqual(visibleDatasetVersions(versions).map(v => v.id), ['r', 'p'])
  assert.deepEqual(visibleDatasetVersions(versions, true), versions)
})

test('sequential cleanup preserves failures with IDs and continues after conflicts', async () => {
  let active = 0
  const calls = []
  const result = await cleanupFailedVersions(['a', 'b', 'c'], async (id) => {
    assert.equal(active++, 0)
    calls.push(id)
    await Promise.resolve()
    active--
    if (id === 'b') throw new Error('版本被训练引用')
    return { deleted: true }
  })
  assert.deepEqual(calls, ['a', 'b', 'c'])
  assert.deepEqual(result.succeeded, ['a', 'c'])
  assert.deepEqual(result.failed, [{ id: 'b', reason: '版本被训练引用' }])
})

test('cleanup rejects invalid batch sizes before issuing requests and verifies deletion', async () => {
  let calls = 0
  const remove = async () => { calls++; return {} }
  await assert.rejects(cleanupFailedVersions([], remove))
  await assert.rejects(cleanupFailedVersions(Array.from({ length: 101 }, (_, i) => String(i)), remove))
  assert.equal(calls, 0)
  assert.deepEqual((await cleanupFailedVersions(['a', 'a'], remove)).failed, [{ id: 'a', reason: '服务端未确认移除，请刷新后核对' }])
  assert.equal(calls, 1)
  assert.match(cleanupNotice, /保留审计.*不删除原始数据、Parquet文件或训练任务.*不释放存储/)
})
