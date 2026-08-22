import assert from 'node:assert/strict'
import test from 'node:test'

import { dataSpaceDirectoriesPath, dataSpaceEntriesPath, dataSpacesPath, workspaceSnapshotsPath } from './dataSpacesPaths.js'

test('data-space API paths encode logical space, relative path, and cursor', () => {
  assert.equal(dataSpacesPath(), '/api/v1/data-spaces')
  assert.equal(
    dataSpaceDirectoriesPath('my-files', 'datasets/a', 'opaque+/='),
    '/api/v1/data-spaces/my-files/directories?path=datasets%2Fa&cursor=opaque%2B%2F%3D',
  )
})

test('data-space directory path omits empty optional query values', () => {
	assert.equal(dataSpaceDirectoriesPath('public'), '/api/v1/data-spaces/public/directories')
})

test('workspace snapshot path never includes object-storage coordinates', () => {
  assert.equal(workspaceSnapshotsPath(), '/api/v1/workspace-snapshots')
  assert.equal(workspaceSnapshotsPath(50), '/api/v1/workspace-snapshots?limit=50')
})

test('data-space file paths stay logical and encode only a relative path', () => {
  assert.equal(
    dataSpaceEntriesPath('my-files', 'datasets/a', 'opaque+/='),
    '/api/v1/data-spaces/my-files/entries?path=datasets%2Fa&cursor=opaque%2B%2F%3D',
  )
})
