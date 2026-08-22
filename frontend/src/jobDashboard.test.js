import test from 'node:test'
import assert from 'node:assert/strict'
import { canOpenRayDashboard, jobDashboardAccessPath } from './jobDashboard.js'

test('dashboard action appears only after the RayCluster name is observed', () => {
  assert.equal(canOpenRayDashboard({ rayClusterName: 'train-a' }), true)
  assert.equal(canOpenRayDashboard({ rayClusterName: '' }), false)
  assert.equal(canOpenRayDashboard(null), false)
})

test('dashboard access uses the authenticated same-origin API', () => {
  assert.equal(jobDashboardAccessPath('job-1'), '/api/v1/jobs/job-1/dashboard-access')
})
