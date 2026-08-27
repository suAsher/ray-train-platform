import assert from 'node:assert/strict'
import test from 'node:test'

import { canCancelJob } from './jobPermissions.js'

test('engineer can stop only an owned active job', () => {
  const engineer = { userId: 'user-a', roles: ['Engineer'] }

  assert.equal(canCancelJob({ userId: 'user-a', status: 'RUNNING' }, engineer), true)
  assert.equal(canCancelJob({ userId: 'user-b', status: 'RUNNING' }, engineer), false)
})

test('tenant and platform administrators can stop jobs in their visible scope', () => {
  const otherJob = { userId: 'user-b', status: 'QUEUED' }

  assert.equal(canCancelJob(otherJob, { userId: 'team-admin', roles: ['TenantAdmin'] }), true)
  assert.equal(canCancelJob(otherJob, { userId: 'platform-admin', roles: ['SuperAdmin'] }), true)
})

test('terminal jobs do not expose a stop action', () => {
  for (const status of ['SUCCEEDED', 'FAILED', 'CANCELED', 'TIMED_OUT']) {
    assert.equal(canCancelJob({ userId: 'user-a', status }, { userId: 'user-a', roles: ['Engineer'] }), false)
  }
})
