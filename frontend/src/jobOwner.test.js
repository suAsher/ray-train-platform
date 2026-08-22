import assert from 'node:assert/strict'
import test from 'node:test'

import { displayJobOwner } from './jobOwner.js'

test('displays the signed-in user as me', () => {
  assert.equal(displayJobOwner('user-1', 'user-1', new Map()), '我')
})

test('resolves another submitter through the user directory', () => {
  const users = new Map([['59778341-opaque-id', 'guofeng.su']])

  assert.equal(displayJobOwner('59778341-opaque-id', 'admin-1', users), 'guofeng.su')
})

test('keeps an opaque owner identifier short when no directory entry exists', () => {
  assert.equal(displayJobOwner('unknown-subject-123', 'admin-1', new Map()), 'unknown-…')
})
