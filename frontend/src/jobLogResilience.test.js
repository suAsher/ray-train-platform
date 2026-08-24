import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./views/Job/JobDetail.vue', import.meta.url), 'utf8')

test('job detail resets log cursors and rejects stale responses when route job changes', () => {
  assert.match(source, /watch\(\(\) => route\.params\.id/)
  assert.match(source, /resetLogState/)
  assert.match(source, /requestJobId !== String\(route\.params\.id\)/)
})

test('a transient log error never hides the existing console', () => {
  assert.doesNotMatch(source, /v-if="!logsError"/)
  assert.match(source, /已加载的日志仍可查看/)
})
