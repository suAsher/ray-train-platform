import test from 'node:test'
import assert from 'node:assert/strict'
import { createDocumentLoader } from './loader.js'
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
test('only the latest request can update documents, errors and loading state', async () => {
  const first = deferred(), second = deferred(), third = deferred()
  const pending = [first, second, third]
  const loader = createDocumentLoader(() => pending.shift().promise)
  const a = loader.load(), b = loader.load()
  first.reject(new Error('outdated failure'))
  await a
  assert.equal(loader.loading.value, true)
  assert.equal(loader.error.value, '')
  second.resolve({ items: [{ id: 'new' }] })
  await b
  assert.deepEqual(loader.documents.value, [{ id: 'new' }])
  assert.equal(loader.loading.value, false)
  const c = loader.load()
  third.reject(new Error('current failure'))
  await c
  assert.equal(loader.error.value, 'current failure')
  assert.deepEqual(loader.documents.value, [])
})
test('a late success cannot resurrect an unpublished document', async () => {
  const first = deferred(), second = deferred(), pending = [first, second]
  const loader = createDocumentLoader(() => pending.shift().promise)
  const a = loader.load(), b = loader.load()
  second.resolve({ items: [] }); await b
  first.resolve({ items: [{ id: 'unpublished' }] }); await a
  assert.deepEqual(loader.documents.value, [])
})
