// The help page and the file its download button produces are built from one
// source, so what a user reads on screen and what they take away cannot drift
// apart. Topics live in topics.js and their snippets in snippets.js; this
// module is the shared entry point and the Markdown serializer.

import { helpSections } from './topics.js'

export { helpSections } from './topics.js'
export * from './snippets.js'

export function renderHelpMarkdown(sections = helpSections) {
  const lines = ['# RayTrain 平台使用说明', '']
  for (const section of sections) {
    lines.push(`## ${section.title}`, '')
    if (section.summary) lines.push(section.summary, '')
    for (const block of section.blocks) {
      lines.push(...renderBlock(block))
    }
  }
  return lines.join('\n').replace(/\n{3,}/g, '\n\n').trimEnd() + '\n'
}

function renderBlock(block) {
  switch (block.kind) {
    case 'steps':
      return block.items.flatMap((item, index) => {
        const head = [`${index + 1}. **${item.title}** — ${item.body}`, '']
        if (!item.code) return head
        return [...head, '```' + (item.codeLang || ''), item.code, '```', '']
      })
    case 'list':
      return [...block.items.map((item) => `- ${item}`), '']
    case 'checklist':
      return [...block.items.map((item) => `- [ ] ${item}`), '']
    case 'table':
      return [
        `| ${block.headers.join(' | ')} |`,
        `| ${block.headers.map(() => '---').join(' | ')} |`,
        ...block.rows.map((row) => `| ${row.join(' | ')} |`),
        '',
      ]
    case 'code':
      return [block.label ? `**${block.label}**` : '', '', '```' + (block.lang || ''), block.text, '```', '']
    case 'warning':
      return [`> **${block.title}**`, '>', `> ${block.text}`, '']
    case 'note':
      return [`> ${block.text}`, '']
    default:
      return []
  }
}
