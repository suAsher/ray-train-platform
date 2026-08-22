import assert from 'node:assert/strict'
import test from 'node:test'

import { formatStorageQuota, storageQuotaGiBFromQuantity, storageQuotaGiBToBytes } from './storageQuota.js'

test('converts only finite whole-GiB administrator requests', () => {
  assert.equal(storageQuotaGiBToBytes(100), 100 * 1024 * 1024 * 1024)
  assert.equal(storageQuotaGiBToBytes(0), null)
  assert.equal(storageQuotaGiBToBytes(-1), null)
  assert.equal(storageQuotaGiBToBytes(1.5), null)
})

test('formats effective hard quota for the administrator table', () => {
  assert.equal(formatStorageQuota(100 * 1024 * 1024 * 1024), '100 GiB')
  assert.equal(formatStorageQuota(2 * 1024 * 1024 * 1024 * 1024), '2 TiB')
  assert.equal(formatStorageQuota(0), '未启用')
})

test('reads the deployment hard limit as a whole GiB value for form validation', () => {
  assert.equal(storageQuotaGiBFromQuantity('2Ti'), 2048)
  assert.equal(storageQuotaGiBFromQuantity('100Gi'), 100)
  assert.equal(storageQuotaGiBFromQuantity(''), null)
  assert.equal(storageQuotaGiBFromQuantity('1.5Ti'), null)
})
