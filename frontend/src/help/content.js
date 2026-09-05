// The help page and the file its download button produces are built from one
// source, so what a user reads on screen and what they take away cannot drift
// apart. Topics live in topics.js and their snippets in snippets.js; this
// module is the shared entry point and the Markdown serializer.

import { helpSections } from './topics.js'

export { helpSections } from './topics.js'
export * from './snippets.js'

export const HELP_REVIEWED_AT = '2026-09-05'
export const HELP_SCOPE = '对照 release-20260905-04；权限与可用运行环境以当前页面为准。Streaming 全量多卡验收仍待完成。'

export function filterHelpSections(query, sections = helpSections) {
  const terms = String(query || '').trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (!terms.length) return [...sections]
  return sections.filter(section => {
    const text = JSON.stringify(section).toLowerCase()
    return terms.every(term => text.includes(term))
  })
}

export function renderHelpMarkdown(sections = helpSections, { origin = '' } = {}) {
  const lines = ['# RayTrain 平台使用说明', '', `内容核对：${HELP_REVIEWED_AT}。${HELP_SCOPE}`, '']
  for (const section of sections) {
    lines.push(`## ${section.title}`, '')
    if (section.summary) lines.push(section.summary, '')
    lines.push(...renderChecklist('开始前', section.prerequisites))
    for (const block of section.blocks) {
      lines.push(...renderBlock(block))
    }
    lines.push(...renderChecklist('成功标志', section.success))
    lines.push(...renderChecklist('失败处理', section.troubleshooting))
    if (section.relatedLinks?.length) {
      lines.push('### 相关入口', '', ...section.relatedLinks.map(link =>
        `- [${link.label}](${origin ? new URL(link.to, origin).href : link.to})`), '')
    }
  }
  return lines.join('\n').replace(/\n{3,}/g, '\n\n').trimEnd() + '\n'
}

function renderChecklist(title, items) {
  return items?.length ? [`### ${title}`, '', ...items.map(item => `- ${item}`), ''] : []
}

const tableCell = value => String(value).replaceAll('|', '\\|').replace(/\r?\n/g, '<br>')

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
        `| ${block.headers.map(tableCell).join(' | ')} |`,
        `| ${block.headers.map(() => '---').join(' | ')} |`,
        ...block.rows.map((row) => `| ${row.map(tableCell).join(' | ')} |`),
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
