import assert from 'node:assert/strict'
import test from 'node:test'

import { queueJobAction, queuePanelStats } from './queuePanelActions.js'

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

test('super administrator can stop active workloads in any tenant', () => {
  assert.deepEqual(queueJobAction({ tenantId: 'team-b', state: 'RUNNING' }, 'team-a', true), {
    kind: 'stop',
    label: '停止任务',
  })
  assert.deepEqual(queueJobAction({ tenantId: 'team-b', state: 'QUEUED' }, 'team-a', true), {
    kind: 'cancel-queue',
    label: '取消排队',
  })
})

test('queue panel distinguishes job reservations from physical GPU allocation', () => {
  assert.deepEqual(queuePanelStats([
    { state: 'RUNNING', gpus: 8 },
    { state: 'PROVISIONING', gpus: 8 },
    { state: 'QUEUED', gpus: 16 },
  ], 32, 24), {
    runningJobs: 1,
    waitingJobs: 2,
    activeRequestedGPUs: 16,
    queuedRequestedGPUs: 16,
    physicalAllocatedGPUs: 24,
    releasingGPUs: 8,
    clusterGPUs: 32,
  })
})
