import assert from 'node:assert/strict'
import test from 'node:test'

import { appendDataSpaceDirectory, parentDataSpaceDirectory, selectedDataSpaceDirectoryLabel } from './dataSpaceSelection.js'

test('data-space directory navigation stays relative to the selected root', () => {
  assert.equal(appendDataSpaceDirectory('', 'datasets'), 'datasets')
  assert.equal(appendDataSpaceDirectory('datasets/v1', 'images'), 'datasets/v1/images')
  assert.equal(parentDataSpaceDirectory('datasets/v1/images'), 'datasets/v1')
  assert.equal(parentDataSpaceDirectory('datasets'), '')
})

test('data-space directory navigation rejects unsafe names from a browser response', () => {
  for (const unsafe of ['', '.', '..', '../other-user', 'nested/path', '\\windows']) {
    assert.throws(() => appendDataSpaceDirectory('datasets', unsafe), /目录名称不合法/)
  }
})

test('selected data-space label describes the logical root without backing storage details', () => {
  assert.equal(selectedDataSpaceDirectoryLabel('我的文件', ''), '我的文件 /')
  assert.equal(selectedDataSpaceDirectoryLabel('团队共享数据', 'datasets/v1'), '团队共享数据 / datasets/v1')
})
