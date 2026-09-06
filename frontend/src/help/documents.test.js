import test from 'node:test'
import assert from 'node:assert/strict'
import * as docs from './documents.js'

test('safe Markdown escapes HTML and refuses executable links and remote image tracking', () => {
  assert.equal(typeof docs.renderDocumentMarkdown, 'function')
  const html = docs.renderDocumentMarkdown('<script>alert(1)</script>\n\n[x](javascript:alert(1))\n\n![tracking](https://evil.test/pixel)\n\n[ok](/help#data-mode)')
  assert.ok(!html.includes('<script>'))
  assert.ok(!html.includes('href="javascript:'))
  assert.ok(!html.includes('<img'))
  assert.ok(html.includes('href="/help#data-mode"'))
})
test('template extraction accepts only simple filenames and preserves exact code', () => {
  const result = docs.documentTemplates('```python filename=train.py\nprint(1)\n```\n\n```sh filename=../../x.sh\necho nope\n```')
  assert.deepEqual(result, [{ filename: 'train.py', text: 'print(1)\n' }])
  assert.deepEqual(docs.documentTemplates('``` filename=.dockerignore\n.git\n```'), [{ filename: '.dockerignore', text: '.git\n' }])
})
test('published docs search and download only operate on supplied published snapshots', () => {
  const published = [{ id: 'first', title: 'First', category: 'Start', markdown: 'Published body', version: 2 }]
  assert.equal(docs.searchDocuments(published, 'published first').length, 1)
  assert.equal(docs.searchDocuments(published, 'secret').length, 0)
  const markdown = docs.downloadDocuments(published)
  assert.match(markdown, /Published body/)
  assert.ok(!markdown.includes('secret'))
})

test('offline download resolves links without changing code, formatting or external URLs', () => {
  const markdown = '[Job](/job) [Topic](/help#data-mode) [External](https://example.test/x)\n\n' +
    '`[code](/job)`\n\n```sh filename=x.sh\n[code](/job)\n```\n\n' +
    '    [indented code](/job)\n\n[Reference][job]\n\n[job]: /job "Jobs"\n\n' +
    '[Nested](</job?q=(x)> "Title")\n'
  const result = docs.downloadDocuments([{ title: 'Title', markdown }], { origin: 'https://platform.test' })
  assert.match(result, /\[Job\]\(https:\/\/platform.test\/job\)/)
  assert.match(result, /\[Topic\]\(https:\/\/platform.test\/help#data-mode\)/)
  assert.ok(result.includes('[External](https://example.test/x)'))
  assert.ok(result.includes('`[code](/job)`'))
  assert.ok(result.includes('```sh filename=x.sh\n[code](/job)\n```'))
  assert.ok(result.includes('    [indented code](/job)'))
  assert.ok(result.includes('[job]: https://platform.test/job "Jobs"'))
  assert.ok(result.includes('[Nested](<https://platform.test/job?q=(x)> "Title")'))
})

test('offline links preserve escaped destinations, CRLF code and paragraph code-span boundaries', () => {
  const markdown = '` unmatched\n\n[go](/job)\n\n` another\n\n' + String.raw`[escaped](/a\() [entity](&#47;job)` + '\n\n```sh\r\n[code](/job)\r\n```\r\n'
  const result = docs.downloadDocuments([{ title: 'Title', markdown }], { origin: 'https://platform.test' })
  assert.ok(result.includes('[go](https://platform.test/job)'))
  assert.ok(result.includes(String.raw`[escaped](https://platform.test/a\()`))
  assert.ok(result.includes('```sh\r\n[code](/job)\r\n```\r\n'))
})

test('unpublished deep links are distinguished from a topic hidden only by search', () => {
  const documents = [{ id: 'one', title: 'Title', markdown: 'Published' }]
  assert.equal(docs.missingDocumentLink(documents, '#one'), false)
  assert.equal(docs.missingDocumentLink(documents, '#removed'), true)
  assert.equal(docs.missingDocumentLink(documents, ''), false)
})

test('offline download handles table and nested list links and keeps code in those blocks intact', () => {
  const markdown = '| Link | Code |\n| --- | --- |\n| [job](/job) | `[code](/job)` |\n\n> - [nested](/help#one)\n'
  const result = docs.downloadDocuments([{ title: 'Title', markdown }], { origin: 'https://platform.test' })
  assert.ok(result.includes('| [job](https://platform.test/job) | `[code](/job)` |'))
  assert.ok(result.includes('> - [nested](https://platform.test/help#one)'))
})
