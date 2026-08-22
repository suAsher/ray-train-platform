import assert from 'node:assert/strict'
import test from 'node:test'

import { previewCommand, entrypointWarnings } from './commandPreview.js'

// Users could not tell what would actually run: they typed `python train.py`
// and the platform silently wrapped it in torchrun. The preview makes the real
// command visible before submitting.
test('single GPU runs the command verbatim on one reserved GPU', () => {
  assert.equal(previewCommand('python train.py --epochs 3', 1, 1), 'python train.py --epochs 3')
})

test('single-node multi-GPU is expanded to the torchrun the platform runs', () => {
  assert.equal(
    previewCommand('python tools/train.py configs/a.yaml', 1, 8),
    'torchrun --standalone --nproc_per_node=8 tools/train.py configs/a.yaml',
  )
})

// A launcher that is not a .py script keeps --no-python, matching
// images/workspace/raytrain-launch.py.
test('a non-script command keeps torchrun in no-python mode', () => {
  assert.equal(
    previewCommand('bash tools/dist_train.sh', 1, 4),
    'torchrun --standalone --nproc_per_node=4 --no-python bash tools/dist_train.sh',
  )
})

test('multi-node shows the per-node torchrun Ray starts on each worker', () => {
  const preview = previewCommand('python tools/train.py', 2, 8)
  assert.match(preview, /--nnodes=2/)
  assert.match(preview, /--nproc_per_node=8/)
  assert.match(preview, /--node_rank=/)
})

// Writing torchrun by hand double-wraps the command and fails at runtime with a
// confusing rendezvous error, so it is caught in the form instead.
test('a hand-written torchrun is reported before submission', () => {
  const warnings = entrypointWarnings('torchrun --nproc_per_node=8 train.py', 1, 8)
  assert.equal(warnings.length, 1)
  assert.match(warnings[0], /不要自己写 torchrun/)
})

test('torchpack dist-run is reported as incompatible with the platform launcher', () => {
  const warnings = entrypointWarnings('torchpack dist-run -np 8 python tools/train.py', 1, 8)
  assert.equal(warnings.length, 1)
  assert.match(warnings[0], /torchpack/)
})

test('shell operators are reported because the platform runs one command', () => {
  const warnings = entrypointWarnings('cd /workspace && python train.py', 1, 1)
  assert.equal(warnings.length, 1)
  assert.match(warnings[0], /&&/)
})

test('a plain single command produces no warnings', () => {
  assert.deepEqual(entrypointWarnings('python tools/train.py configs/a.yaml --launcher pytorch', 1, 8), [])
})
