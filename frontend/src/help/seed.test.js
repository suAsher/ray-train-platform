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

test('embedded seed faithfully contains every stable topic without overwriting metadata', () => {
  const seed = JSON.parse(readFileSync(new URL('../../../backend/helpdocs/seed.json', import.meta.url), 'utf8'))
  assert.equal(seed.length, content.helpSections.length)
  assert.equal(new Set(seed.map(item => item.id)).size, seed.length)
  assert.deepEqual(seed, content.helpSections.map((section, index) => ({
    id: section.id, title: section.title, category: section.group || '其他',
    sortOrder: index * 10, markdown: content.renderHelpSectionMarkdown(section),
  })))
  for (const item of seed) {
    assert.match(item.id, /^[a-z0-9][a-z0-9-]{0,79}$/)
    assert.ok(Buffer.byteLength(item.markdown) < 512 * 1024)
  }
})
