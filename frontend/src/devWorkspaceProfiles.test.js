import assert from 'node:assert/strict'
import test from 'node:test'

import { interactiveWorkspaceProfiles, workspaceProfileForGPUCount } from './devWorkspaceProfiles.js'

test('interactive workspace profiles reserve one GPU worker with an explicit local GPU count', () => {
  assert.deepEqual(interactiveWorkspaceProfiles.map((profile) => profile.gpuCount), [1, 2, 4, 8])
  assert.equal(workspaceProfileForGPUCount(8).topology, '1 节点 × 8 GPU')
})
