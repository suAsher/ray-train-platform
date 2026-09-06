import test from 'node:test'
import assert from 'node:assert/strict'
import * as editor from './editor.js'

test('editor preserves unsaved text on conflict and blocks publication before save', async () => {
  assert.equal(typeof editor.createDocumentEditor, 'function')
  let publishes = 0
  const model = editor.createDocumentEditor({ saveHelpDocument: async () => { throw new Error('conflict') }, publishHelpDocument: async () => { publishes++ } })
  model.select({ id: 'first', title: 'Title', category: 'Start', sortOrder: 0, markdown: 'published', version: 2 })
  model.form.value = { ...model.form.value, markdown: 'local draft' }
  await assert.rejects(model.save(), /conflict/)
  assert.equal(model.form.value.markdown, 'local draft')
  assert.equal(model.dirty.value, true)
  await assert.rejects(model.publish(), /保存/)
  assert.equal(publishes, 0)
})
test('create saves draft and publish uses latest expectedVersion', async () => {
  const calls = []
  const model = editor.createDocumentEditor({
    createHelpDocument: async doc => ({ ...doc, version: 1, publishedVersion: 0 }),
    publishHelpDocument: async (id, version) => { calls.push([id, version]); return { ...model.form.value, version: 2, publishedVersion: 2 } },
  })
  model.select(null)
  model.form.value = { ...model.form.value, id: 'new-doc', title: 'New', category: 'Start', markdown: 'Body' }
  await model.save()
  assert.equal(model.form.value.publishedVersion, 0)
  assert.equal(model.dirty.value, false)
  await model.publish()
  assert.deepEqual(calls, [['new-doc', 1]])
})

test('restore and unpublish advance versions without silently publishing a draft', async () => {
  const calls = []
  const model = editor.createDocumentEditor({
    restoreHelpDocument: async (id, expectedVersion, restoreVersion) => {
      calls.push(['restore', id, expectedVersion, restoreVersion])
      return { ...model.form.value, markdown: 'restored', version: 4 }
    },
    unpublishHelpDocument: async (id, expectedVersion) => {
      calls.push(['unpublish', id, expectedVersion])
      return { ...model.form.value, version: 5, publishedVersion: 0 }
    },
  })
  model.select({ id: 'first', title: 'Title', category: 'Start', markdown: 'new', sortOrder: 0, version: 3, publishedVersion: 2 })
  model.form.value.markdown = 'unsaved'
  await assert.rejects(model.restore(1), /保存/)
  await assert.rejects(model.unpublish(), /保存/)
  model.form.value.markdown = 'new'
  await model.restore(1)
  assert.equal(model.form.value.publishedVersion, 2)
  assert.equal(model.form.value.markdown, 'restored')
  await model.unpublish()
  assert.equal(model.form.value.publishedVersion, 0)
  assert.deepEqual(calls, [['restore', 'first', 3, 1], ['unpublish', 'first', 4]])
})

test('editor validates all fields before attempting a request', async () => {
  const model = editor.createDocumentEditor({ createHelpDocument: () => { throw new Error('must not call API') } })
  const valid = { id: 'first', title: 'Title', category: 'Start', markdown: 'body', sortOrder: 0 }
  for (const patch of [{ id: '../bad' }, { title: '' }, { category: '' }, { markdown: '' }, { markdown: 'x'.repeat(512 * 1024 + 1) }, { sortOrder: 0.5 }, { sortOrder: 100001 }]) {
    model.select({ ...valid, ...patch })
    await assert.rejects(model.save(), err => !err.message.includes('must not call API'))
  }
})
