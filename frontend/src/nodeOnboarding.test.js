import test from 'node:test'
import assert from 'node:assert/strict'
import { nodeOnboardingRows, refreshNodeTopology } from './nodeOnboarding.js'

test('node rows distinguish disabled, verification, readiness and cordon', () => {
  const rows = nodeOnboardingRows([
    { nodeName: 'new', capacity: 8 },
    { nodeName: 'pending', capacity: 8, nodeReady: true, cordoned: true, onboardingStage: 'probe', cacheReady: false, onboardingReason: '<script>unsafe</script>' },
    { nodeName: 'done', capacity: 8, nodeReady: true, cordoned: false, onboardingStage: 'ready', cacheReady: true },
  ])
  assert.equal(rows[0].stage, '未启用自动验收 / 未验收')
  assert.equal(rows[1].stage, '挂载与读写验收中')
  assert.equal(rows[1].scheduling, '已暂停调度')
  assert.equal(rows[1].reason, '<script>unsafe</script>')
  assert.equal(rows[2].stage, '存储验收通过')
  assert.equal(nodeOnboardingRows([{ onboardingStage: 'ready', cacheReady: false }])[0].stage, '待重新验收')
})

test('failed topology refresh clears previous nodes and reports unknown', async () => {
  const result = await refreshNodeTopology(async () => { throw new Error('unavailable') })
  assert.deepEqual(result.nodes, [])
  assert.equal(result.available, false)
  assert.equal(result.physicalGPUs, null)
  assert.match(result.error, /未知/)
  const ok = await refreshNodeTopology(async () => ({ nodes: [{nodeName:'gpu',capacity:8}], totalGpus:8, usedGpus:1 }))
  assert.equal(ok.available, true)
  assert.equal(ok.nodes[0].nodeName, 'gpu')
})
