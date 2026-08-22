import assert from 'node:assert/strict'
import test from 'node:test'

import { finishedLabel, formatDuration, jobTimeline, originLabel } from './jobTimeline.js'

test('job timeline labels origin and duration', () => {
  assert.equal(formatDuration('2026-08-19T00:00:00Z', '2026-08-19T00:01:05Z'), '1 分 05 秒')
  assert.equal(originLabel('ray-cli'), 'spk-rayjob 外部提交')
})

// A job that has not finished has no end time. Printing "运行中" in the end
// column made a queued job look like it was training.
test('an absent end time renders as unknown, not as running', () => {
  assert.equal(finishedLabel(null), '—')
  assert.equal(finishedLabel('2026-08-19T00:01:05Z').length > 0, true)
})

// Queue wait and training time are different facts. Reporting one number
// measured from submission made a job that waited an hour for a GPU and then
// trained for two minutes report an hour of "training".
test('queue wait and training time are reported separately', () => {
  const timeline = jobTimeline({
    createdAt: '2026-08-17T09:00:00Z',
    startedAt: '2026-08-17T09:15:28Z',
    finishedAt: '2026-08-17T09:17:18Z',
  })
  assert.equal(timeline.queuedSeconds, 928)
  assert.equal(timeline.runningSeconds, 110)
  assert.equal(timeline.runningLabel, '1 分 50 秒')
  assert.equal(timeline.isRunning, false)
  assert.equal(timeline.isWaiting, false)
})

test('a running job accrues training time against now', () => {
  const timeline = jobTimeline(
    { createdAt: '2026-08-17T09:00:00Z', startedAt: '2026-08-17T09:15:00Z' },
    '2026-08-17T09:20:00Z',
  )
  assert.equal(timeline.runningSeconds, 300)
  assert.equal(timeline.isRunning, true)
  assert.equal(timeline.finishedAt, null)
})

test('a queued job accrues queue time and reports no training time', () => {
  const timeline = jobTimeline({ createdAt: '2026-08-17T09:00:00Z' }, '2026-08-17T09:05:00Z')
  assert.equal(timeline.queuedSeconds, 300)
  assert.equal(timeline.runningSeconds, null)
  assert.equal(timeline.runningLabel, '—')
  assert.equal(timeline.isWaiting, true)
})

// Jobs recorded before the platform captured start times keep an end time but
// no start. Their queue and training split is genuinely unknown and must not be
// invented from the submission timestamp.
test('a legacy job without a start time reports unknown spans rather than guessing', () => {
  const timeline = jobTimeline({ createdAt: '2026-08-17T09:00:00Z', finishedAt: '2026-08-17T09:17:18Z' })
  assert.equal(timeline.queuedSeconds, null)
  assert.equal(timeline.runningSeconds, null)
  assert.equal(timeline.queuedLabel, '—')
  assert.equal(timeline.isRunning, false)
})

test('snake_case payloads are accepted alongside camelCase', () => {
  const timeline = jobTimeline({
    created_at: '2026-08-17T09:00:00Z',
    started_at: '2026-08-17T09:10:00Z',
    finished_at: '2026-08-17T09:12:00Z',
  })
  assert.equal(timeline.queuedSeconds, 600)
  assert.equal(timeline.runningSeconds, 120)
})
