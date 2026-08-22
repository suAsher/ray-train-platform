import assert from 'node:assert/strict'
import test from 'node:test'

import { queueJobAction } from './queuePanelActions.js'

test('current-tenant queued and active workloads get distinct actions', () => {
  assert.deepEqual(queueJobAction({ tenantId: 'team-a', state: 'QUEUED' }, 'team-a'), {
    kind: 'cancel-queue',
    label: '取消排队',
  })
  assert.deepEqual(queueJobAction({ tenantId: 'team-a', state: 'RUNNING' }, 'team-a'), {
    kind: 'stop',
    label: '停止任务',
  })
  assert.deepEqual(queueJobAction({ tenantId: 'team-a', state: 'PROVISIONING' }, 'team-a'), {
    kind: 'stop',
    label: '停止任务',
  })
})

test('cross-tenant and unidentified workloads are read-only', () => {
  assert.equal(queueJobAction({ tenantId: 'team-b', state: 'RUNNING' }, 'team-a'), null)
  assert.equal(queueJobAction({ tenantId: 'team-a', state: 'RUNNING' }, ''), null)
  assert.equal(queueJobAction({ tenantId: 'team-a', state: 'SUCCEEDED' }, 'team-a'), null)
})
