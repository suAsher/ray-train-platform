import assert from 'node:assert/strict'
import test from 'node:test'

import { adminNavigation } from './adminNav.js'

test('administrator navigation groups every platform control surface in the UI', () => {
  assert.deepEqual(adminNavigation.map((item) => item.id), ['console', 'cluster', 'nodes'])
  assert.equal(adminNavigation[0].label, '管理员控制台')
})
