import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { loadAdminActiveJobs, refreshAdminActiveJobs } from './adminActiveJobs.js'

test('admin queue requests team scope instead of silently defaulting to mine', async () => {
  const requests = []
  const apiGet = async (url) => { requests.push(url); return { items: [], total: 0 } }
  await loadAdminActiveJobs(apiGet)
  assert.ok(requests.length > 0)
  for (const url of requests) assert.equal(new URL(url, 'https://platform.test').searchParams.get('scope'), 'team')
})

test('admin queue follows every page and de-duplicates jobs moving between states', async () => {
  const offsets = []
  const jobs = await loadAdminActiveJobs(async (url) => {
    const query = new URL(url, 'https://platform.test').searchParams
    const state = query.get('status')
    const offset = Number(query.get('offset'))
    if (state === 'RUNNING') {
      offsets.push(offset)
      return { total: 3, items: offset === 0 ? [job('one'), job('two')] : [job('three')] }
    }
    return { total: state === 'RECOVERING' ? 1 : 0, items: state === 'RECOVERING' ? [job('one', 'RECOVERING')] : [] }
  })
  assert.deepEqual(offsets, [0, 2])
  assert.equal(jobs.length, 3)
  assert.equal(jobs.find((item) => item.id === 'one').state, 'RECOVERING')
  assert.equal(jobs[0].gpus, 8)
})

test('any failed page preserves previous successful results and reports the failure', async () => {
  const previous = { jobs: [job('previous')], available: true, error: '' }
  const next = await refreshAdminActiveJobs(async () => { throw new Error('network failed') }, previous)
  assert.deepEqual(next.jobs, previous.jobs)
  assert.equal(next.available, true)
  assert.equal(next.error, 'network failed')
  assert.equal(previous.error, '')
})

test('failure on a later page does not replace previous results with the first page', async () => {
  const previous = { jobs: [job('previous')], available: true, error: '' }
  const next = await refreshAdminActiveJobs(async (url) => {
    const query = new URL(url, 'https://platform.test').searchParams
    if (query.get('status') !== 'RUNNING') return { items: [], total: 0 }
    if (query.get('offset') === '0') return { items: [job('partial')], total: 2 }
    throw new Error('second page failed')
  }, previous)
  assert.deepEqual(next.jobs, previous.jobs)
  assert.equal(next.error, 'second page failed')
})

test('initial failure is unavailable rather than a zero job count; successful retry clears error', async () => {
  const failed = await refreshAdminActiveJobs(async () => { throw new Error('offline') })
  assert.equal(failed.available, false)
  const next = await refreshAdminActiveJobs(async () => ({ items: [], total: 0 }), failed)
  assert.equal(next.available, true)
  assert.equal(next.error, '')
})

test('malformed and incomplete pages fail closed instead of publishing partial counts', async () => {
  for (const page of [undefined, { items: [] }, { items: [], total: 10 }]) {
    await assert.rejects(loadAdminActiveJobs(async () => page))
  }
})

test('ever-growing totals stop at a bounded page count', async () => {
  let runningCalls = 0
  await assert.rejects(loadAdminActiveJobs(async (url) => {
    const state = new URL(url, 'https://platform.test').searchParams.get('status')
    if (state !== 'RUNNING') return { items: [], total: 0 }
    runningCalls += 1
    if (runningCalls > 1000) throw new Error('unbounded requests')
    return { items: [job(String(runningCalls))], total: runningCalls + 1 }
  }), /分页上限/)
  assert.equal(runningCalls, 1000)
})

test('queue wires availability and errors and hides initial unavailable counts', async () => {
  const view = await readFile(new URL('./views/QuotaManage/index.vue', import.meta.url), 'utf8')
  const panel = await readFile(new URL('./components/admin/QueuePanel.vue', import.meta.url), 'utf8')
  assert.match(view, /refreshAdminActiveJobs\(apiGet, activeJobState\.value\)/)
  assert.match(view, /:jobs-available="activeJobState.available"/)
  assert.match(view, /:jobs-error="activeJobState.error"/)
  assert.match(panel, /v-if="jobsError"/)
  assert.match(panel, /v-if="jobsAvailable"/)
  assert.match(panel, /:disabled="Boolean\(jobsError\)"/)
  assert.match(panel, /props.jobsAvailable \? stats.runningJobs : '—'/)
})

function job(id, state = 'RUNNING') {
  return { id, tenantId: 'other-team', observedState: state, spec: { resources: { workerReplicas: 2, gpusPerWorker: 4 } } }
}
