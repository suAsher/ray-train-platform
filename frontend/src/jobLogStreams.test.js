import test from 'node:test'
import assert from 'node:assert/strict'

import { buildLogStreamCards } from './jobLogStreams.js'

test('explains submitter, head, and worker log streams instead of exposing raw pod names as roles', () => {
  const cards = buildLogStreamCards([
    'rayjob-abc-ctxl5',
    'rayjob-abc-raycluster-hfpqj-head-gwvk5',
    'rayjob-abc-raycluster-hfpqj-w-worker-lzn4x',
    'rayjob-abc-raycluster-hfpqj-w-worker-tng9z',
  ], 16)

  assert.deepEqual(cards.map((card) => card.label), [
    '全部训练日志',
    '任务提交器',
    'Ray Head',
    '训练 Worker 1',
    '训练 Worker 2',
  ])
  assert.equal(cards[0].sub, '4 个日志流 · 2 个训练 Worker · 16 张 GPU')
  assert.match(cards[1].sub, /创建 RayJob/)
  assert.match(cards[2].sub, /调度与 Dashboard/)
  assert.match(cards[3].sub, /用户训练代码/)
  assert.equal(cards[3].pod, 'rayjob-abc-raycluster-hfpqj-w-worker-lzn4x')
})
