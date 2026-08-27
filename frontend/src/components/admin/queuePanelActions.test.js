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
	assert.deepEqual(queueJobAction({ tenantId: 'team-a', state: 'RECOVERING' }, 'team-a'), {
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
	{ id: 'job-running', state: 'RUNNING', gpus: 8 },
	{ id: 'job-recovering', state: 'RECOVERING', gpus: 8 },
    { state: 'PROVISIONING', gpus: 8 },
    { state: 'QUEUED', gpus: 16 },
	], 40, 32), {
	runningJobs: 2,
    waitingJobs: 2,
	activeRequestedGPUs: 24,
    queuedRequestedGPUs: 16,
	physicalAllocatedGPUs: 32,
    releasingGPUs: 8,
	clusterGPUs: 40,
  })
})

test('queue panel does not double count one platform job during a state-page race', () => {
	assert.deepEqual(queuePanelStats([
		{ id: 'job-1', state: 'RUNNING', gpus: 16 },
		{ id: 'job-1', state: 'RUNNING', gpus: 16 },
	], 16, 16), {
		runningJobs: 1,
		waitingJobs: 0,
		activeRequestedGPUs: 16,
		queuedRequestedGPUs: 0,
		physicalAllocatedGPUs: 16,
		releasingGPUs: 0,
		clusterGPUs: 16,
	})
})
