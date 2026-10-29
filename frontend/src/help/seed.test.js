import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import * as content from './content.js'

test('individual help Markdown keeps walkthroughs, links and downloadable templates', () => {
  assert.equal(typeof content.renderHelpSectionMarkdown, 'function')
  const markdown = content.renderHelpSectionMarkdown({
    title: 'Example', summary: 'Summary', prerequisites: ['Ready'],
    blocks: [{ kind: 'code', label: 'Template', lang: 'python', filename: 'train.py', text: 'print(1)' }],
    success: ['Done'], troubleshooting: ['Retry'], relatedLinks: [{ label: 'Help', to: '/help#quickstart' }],
  })
  for (const text of ['Summary', '### 开始前', '### 成功标志', '### 失败处理', '[Help](/help#quickstart)', '```python filename=train.py\nprint(1)\n```']) assert.ok(markdown.includes(text), text)
  assert.ok(!markdown.includes('# RayTrain 平台使用说明'))
})

test('independently maintained server seed preserves frontend topic IDs and valid content', () => {
  const seed = JSON.parse(readFileSync(new URL('../../../backend/helpdocs/seed.json', import.meta.url), 'utf8'))
  assert.equal(new Set(seed.map(item => item.id)).size, seed.length)
  // Server documents have their own reading categories, revisions and richer
  // deployment guidance. The local authoring catalogue must not overwrite them.
  assert.deepEqual(seed.map(item => item.id).sort(), [...content.helpSections.map(section => section.id), 'scheduling-topology'].sort())
  for (const item of seed) {
    assert.match(item.id, /^[a-z0-9][a-z0-9-]{0,79}$/)
    assert.ok(item.title.trim() && item.category.trim() && item.markdown.trim())
    assert.ok(Number.isInteger(item.sortOrder))
    assert.ok(Buffer.byteLength(item.markdown) < 512 * 1024)
    assert.doesNotMatch(item.markdown, /wellspiking\.ai|\/mnt\/\.spk-hybrid|同步到功能仓/)
  }
  assert.match(seed.find(item => item.id === 'storage').markdown, /PVC/)
  assert.ok(!seed.some(item => item.id === 'idc-sync-lifecycle'))
  assert.ok(!content.helpSections.some(item => item.id === 'idc-sync-lifecycle'))
})

test('published help is loaded from the API without client catalogue replacement', () => {
  const view = readFileSync(new URL('../views/Help/index.vue', import.meta.url), 'utf8')
  assert.match(view, /createDocumentLoader\(listHelpDocuments\)/)
  assert.match(view, /downloadDocuments\(documents\.value/)
  assert.doesNotMatch(view, /import.*(?:helpSections|cliOnboarding|renderHelpMarkdown)/)
})

test('standalone submission presents PVC assets and optional object storage separately', () => {
  const view = readFileSync(new URL('../components/job/StepData.vue', import.meta.url), 'utf8')
  assert.match(view, /StorageDirectoryPicker v-model="form\.datasetStorage"/)
  assert.match(view, /StorageDirectoryPicker v-model="form\.outputStorage"/)
  assert.match(view, /需要管理员配置/)
  assert.doesNotMatch(view, /IDC|wellspiking/)
})
