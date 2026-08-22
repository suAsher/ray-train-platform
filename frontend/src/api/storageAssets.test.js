import assert from 'node:assert/strict'
import test from 'node:test'

import { storageAssetDirectoriesPath, storageAssetListPath } from './storageAssetPaths.js'

test('storage asset API paths keep kind, relative path, and cursor encoded', () => {
  assert.equal(storageAssetListPath('dataset'), '/api/v1/storage-assets?kind=dataset')
  assert.equal(storageAssetDirectoriesPath('asset-a', 'train v1', 'opaque+/='), '/api/v1/storage-assets/asset-a/directories?path=train+v1&cursor=opaque%2B%2F%3D')
})

test('storage asset directory path omits empty optional query values', () => {
  assert.equal(storageAssetDirectoriesPath('asset-a'), '/api/v1/storage-assets/asset-a/directories')
})
