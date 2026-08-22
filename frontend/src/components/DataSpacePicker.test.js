import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import * as dataSpaceActions from '../dataSpaceActions.js'

test('picker access labels use authoritative write capability for the current session', () => {
  assert.equal(dataSpaceActions.dataSpaceAccessLabel({ id: 'team-shared', provider: 'tos', readOnly: true, canWrite: true }, { roles: ['TenantAdmin'] }), '管理员可发布 · Pod 只读')
  assert.equal(dataSpaceActions.dataSpaceAccessLabel({ id: 'team-shared', provider: 'tos', readOnly: true, canWrite: false }, { roles: ['Engineer'] }), '只读')
  assert.equal(dataSpaceActions.dataSpaceAccessLabel({ id: 'my-files', provider: 'tos', readOnly: false, canWrite: true }, { roles: ['Engineer'] }), '可写')

  const picker = readFileSync(new URL('./DataSpacePicker.vue', import.meta.url), 'utf8')
  assert.match(picker, /dataSpaceAccessLabel/)
  assert.match(picker, /dataSpaceAccessType/)
  assert.doesNotMatch(picker, /space\.readOnly \? '只读' : '可写'/)
})

test('picker keeps readiness-gated selection behavior', () => {
  const picker = readFileSync(new URL('./DataSpacePicker.vue', import.meta.url), 'utf8')
  assert.match(picker, /:disabled="!isReady\(space\)"/)
  assert.match(picker, /if \(!isReady\(space\)\) return\s+model\.value = \{ spaceId: space\.id, relativePath: '' \}/)
})
