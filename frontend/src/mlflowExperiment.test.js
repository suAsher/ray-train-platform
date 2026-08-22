import test from 'node:test'
import assert from 'node:assert/strict'
import { latestMetric, metricSeries, sparklinePoints } from './mlflowExperiment.js'

const experiment = {
  run: { latest: { loss: 1.25, learning_rate: 0.0005, epoch: 3 } },
  series: [
    { key: 'learning_rate', points: [{ step: 1, value: 0.001 }, { step: 2, value: 0.0005 }] },
    { key: 'loss', points: [{ step: 1, value: 4 }, { step: 2, value: 1 }] },
  ],
}

test('selects latest MLflow metric without inventing zero values', () => {
  assert.equal(latestMetric(experiment, ['train_loss', 'loss']), 1.25)
  assert.equal(latestMetric(experiment, ['missing']), null)
})

test('selects a named series and renders bounded sparkline points', () => {
  assert.equal(metricSeries(experiment, ['train_loss', 'loss']).key, 'loss')
  assert.equal(sparklinePoints(metricSeries(experiment, ['loss']).points, 100, 40), '0,0 100,40')
  assert.equal(sparklinePoints([], 100, 40), '')
})
