import assert from 'node:assert/strict'
import test from 'node:test'

import { productionTrainingProfiles } from './trainingProfiles.js'

test('production training profiles state the execution contract, not only GPU counts', () => {
  assert.deepEqual(
    productionTrainingProfiles.map(({ name, workers, gpus, executionMode }) => ({ name, workers, gpus, executionMode })),
    [
      { name: '单卡', workers: 1, gpus: 1, executionMode: 'single_gpu' },
      { name: '单机多卡 DDP', workers: 1, gpus: 8, executionMode: 'torchrun' },
      { name: '多机多卡 Ray Train', workers: 2, gpus: 8, executionMode: 'ray_train' },
    ],
  )
  assert.equal(productionTrainingProfiles.some((profile) => profile.workers * profile.gpus > 16), false)
})
