import assert from 'node:assert/strict'
import test from 'node:test'

import {
  allocationSummary,
	allocationStateTag,
  formatAllocationDuration,
  normalizeGPUAllocations,
} from './gpuAllocations.js'

test('normalizes allocation records without mutating the API payload', () => {
  const payload = [{
    id: 'ws-1', type: 'DEBUG_WORKSPACE', name: 'dev-1', tenantId: 'team-a',
    userId: 'user-1', username: 'alice', state: 'RUNNING', gpuCount: 2,
    namespace: 'tenant-team-a', resourceName: 'dev-1', createdAt: '2026-08-24T08:00:00Z',
  }]
  const before = structuredClone(payload)

  const result = normalizeGPUAllocations(payload)

  assert.deepEqual(payload, before)
  assert.deepEqual(result[0], {
    id: 'ws-1', type: 'DEBUG_WORKSPACE', name: 'dev-1', tenantId: 'team-a',
    userId: 'user-1', username: 'alice', state: 'RUNNING', gpuCount: 2,
    namespace: 'tenant-team-a', resourceName: 'dev-1', createdAt: '2026-08-24T08:00:00Z',
    startedAt: '',
  })
})

test('recovering training allocation remains visibly active', () => {
	assert.equal(allocationStateTag('RECOVERING'), 'success')
})

test('summarizes training and debug occupancy separately', () => {
  const summary = allocationSummary([
    { type: 'TRAINING_JOB', gpuCount: 16 },
    { type: 'DEBUG_WORKSPACE', gpuCount: 2 },
    { type: 'DEBUG_WORKSPACE', gpuCount: 1 },
  ])
  assert.deepEqual(summary, { trainingJobs: 1, debugWorkspaces: 2, detailedGPUs: 19 })
})

test('formats active duration from startedAt and falls back to createdAt', () => {
  const now = new Date('2026-08-24T10:05:06Z')
  assert.equal(formatAllocationDuration({ startedAt: '2026-08-24T09:00:00Z' }, now), '1小时5分钟')
  assert.equal(formatAllocationDuration({ createdAt: '2026-08-24T10:04:00Z' }, now), '1分钟')
  assert.equal(formatAllocationDuration({}, now), '—')
})

test('rejects malformed envelopes and clamps invalid GPU counts', () => {
  assert.deepEqual(normalizeGPUAllocations(null), [])
  assert.deepEqual(normalizeGPUAllocations({ items: 'invalid' }), [])
  assert.equal(normalizeGPUAllocations([{ id: 'x', gpuCount: -8 }])[0].gpuCount, 0)
})
