import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const jobView = await readFile(new URL('./views/Job/index.vue', import.meta.url), 'utf8')

test('job list presents recovery as an active stoppable state', () => {
	assert.match(jobView, /ACTIVE_STATES[^\n]*RECOVERING/)
	assert.match(jobView, /RECOVERING:\s*'恢复中'/)
})
