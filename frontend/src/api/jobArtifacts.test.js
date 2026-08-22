import assert from 'node:assert/strict'
import test from 'node:test'

import { jobArtifactPreviewPath, jobArtifactsPath } from './jobArtifactPaths.js'

test('job artifact API paths encode task-relative values without object-store details', () => {
  assert.equal(jobArtifactsPath('job-a', 'checkpoints/v 1', 'opaque+/=', 50), '/api/v1/jobs/job-a/artifacts?path=checkpoints%2Fv+1&cursor=opaque%2B%2F%3D&limit=50')
  assert.equal(jobArtifactPreviewPath('job-a', 'metrics.json'), '/api/v1/jobs/job-a/artifacts/preview?path=metrics.json')
})

test('job artifact paths omit empty optional directory parameters', () => {
  assert.equal(jobArtifactsPath('job a'), '/api/v1/jobs/job%20a/artifacts')
})
