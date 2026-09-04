import assert from 'node:assert/strict'
import test from 'node:test'

import { interactiveWorkspaceProfiles, workspaceProfileForGPUCount } from './devWorkspaceProfiles.js'

test('interactive workspace profiles reserve one GPU worker with an explicit local GPU count', () => {
  assert.deepEqual(interactiveWorkspaceProfiles.map((profile) => profile.gpuCount), [0, 1, 2, 4, 8])
  assert.equal(workspaceProfileForGPUCount(8).topology, '1 节点 × 8 GPU')
})

// Training can hold every GPU for hours, and a debug session is still needed in
// that window to read data, check paths and install dependencies. Zero has to
// survive the lookup, which is easy to break because it is also falsy.
test('a zero-GPU workspace is offered and resolves to itself, not to one GPU', () => {
  const noGPU = interactiveWorkspaceProfiles.find((profile) => profile.gpuCount === 0)
  assert.ok(noGPU, 'there is no CPU-only debug option')
  assert.equal(workspaceProfileForGPUCount(0).gpuCount, 0)
  assert.equal(workspaceProfileForGPUCount(0).id, noGPU.id)
})
