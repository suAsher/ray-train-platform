import test from 'node:test'
import assert from 'node:assert/strict'
import { helpSections } from './help/topics.js'
import { SUBMIT_MULTINODE, SUBMIT_SMOKE, SUBMIT_STREAMING, RESUME_CODE } from './help/snippets.js'

test('every walkthrough includes prerequisites, verification, recovery and valid destinations', () => {
  const routes = new Set(['/job', '/job/create', '/experiments', '/quota', '/devcenter', '/datacache', '/datasets', '/external-submit', '/account-security', '/help'])
  assert.equal(helpSections.length, 20)
  for (const section of helpSections) {
    for (const key of ['prerequisites', 'success', 'troubleshooting']) {
      assert.ok(section[key]?.length > 0, `${section.id}: ${key}`)
    }
    assert.ok(section.relatedLinks?.length > 0, section.id)
    for (const link of section.relatedLinks) assert.ok(routes.has(link.to), `${section.id}: ${link.to}`)
  }
})

test('submission examples explicitly set their resource scale and site scope', () => {
  assert.match(SUBMIT_SMOKE, /--workers 1 --gpus-per-worker 1/)
  assert.match(SUBMIT_MULTINODE, /--workers 2 --gpus-per-worker 2/)
  assert.match(SUBMIT_STREAMING, /--dataset-sites/)
})

test('upload and streaming guides state limits and incompatible legacy metadata', () => {
  const text = (id) => JSON.stringify(helpSections.find((section) => section.id === id))
  assert.match(text('uploads'), /32 MiB/)
  assert.match(text('uploads'), /重新选择同一文件/)
  assert.match(text('uploads'), /容量/)
  assert.match(text('streaming'), /site_id/)
  assert.match(text('streaming'), /protocol 1/)
  assert.doesNotMatch(text('streaming'), /消费方式与 ray-data 相同/)
  assert.doesNotMatch(text('preflight'), /所有 rank 都用 DistributedSampler/)
  assert.match(RESUME_CODE, /optimizer.load_state_dict/)
  assert.match(RESUME_CODE, /scheduler.load_state_dict/)
})
