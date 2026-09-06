import MarkdownIt from 'markdown-it'

// No raw HTML, plugins, embedded images or scriptable URLs. Preview and reader
// use the same renderer and never trust stored HTML.
const parser = new MarkdownIt({ html: false, linkify: false, typographer: false })
parser.disable('image')
parser.validateLink = url => !/[\u0000-\u0020]/.test(url) && !/^\/\//.test(url) && (!/^[a-z][a-z\d+.-]*:/i.test(url) || /^(https?:|mailto:)/i.test(url))
const escape = parser.utils.escapeHtml
parser.renderer.rules.fence = (tokens, index, _options, env) => {
  const token = tokens[index]
  const codeIndex = env.codes.push(token.content) - 1
  const filename = templateFilename(token.info)
  return `<div class="help-code"><pre><code>${escape(token.content)}</code></pre><button type="button" data-help-copy="${codeIndex}">复制代码</button>${filename ? `<button type="button" data-help-download="${codeIndex}" data-help-filename="${escape(filename)}">下载 ${escape(filename)}</button>` : ''}</div>`
}
function templateFilename(info) {
  const filename = String(info).match(/(?:^|\s)filename=([^\s]+)(?:\s|$)/)?.[1] || ''
  return /^\.?[a-zA-Z0-9][a-zA-Z0-9_.-]{0,118}$/.test(filename) && !filename.includes('..') ? filename : ''
}
export function renderDocumentMarkdown(markdown, env = { codes: [] }) {
  return parser.render(String(markdown || ''), env)
}
export function documentTemplates(markdown) {
  return parser.parse(markdown, {}).filter(token => token.type === 'fence' && templateFilename(token.info))
    .map(token => ({ filename: templateFilename(token.info), text: token.content }))
}
export function searchDocuments(documents, query) {
  const terms = String(query || '').toLowerCase().trim().split(/\s+/).filter(Boolean)
  return documents.filter(doc => terms.every(term => `${doc.title} ${doc.category} ${doc.markdown}`.toLowerCase().includes(term)))
}
export function missingDocumentLink(documents, hash) {
  return !!hash && hash !== '#' && !documents.some(doc => '#' + doc.id === hash)
}
export function downloadDocuments(documents, { origin = '' } = {}) {
  return '# RayTrain 平台使用说明\n\n' + documents.map(doc => `## ${doc.title}\n\n${portableMarkdown(doc.markdown, origin)}`).join('\n\n')
}

// Keep the original Markdown byte-for-byte except for link destinations. The
// block parser identifies code/reference ranges; the inline parser skips code
// spans and escapes. A blanket URL regex would also corrupt runnable examples.
const locator = new MarkdownIt({ html: false }).disable(['image', 'strip_references'])
locator.validateLink = parser.validateLink
locator.inline.ruler.before('link', 'portable_destination', (state, silent) => {
  if (silent || !state.env.record || state.src[state.pos] !== '[') return false
  const end = state.md.helpers.parseLinkLabel(state, state.pos, true)
  if (end < 0 || state.src[end + 1] !== '(') return false
  const start = skipSpace(state.src, end + 2)
  const destination = state.md.helpers.parseLinkDestination(state.src, start, state.posMax)
  if (!destination.ok) return false
  let closing = skipSpace(state.src, destination.pos)
  if (closing !== destination.pos) {
    const title = state.md.helpers.parseLinkTitle(state.src, closing, state.posMax)
    if (title.ok) closing = skipSpace(state.src, title.pos)
  }
  if (state.src[closing] === ')') state.env.record(start, destination.pos, destination.str)
  return false
})
function skipSpace(source, position) {
  while (/[\t\n\r ]/.test(source[position] || '\0')) position++
  return position
}
function portableMarkdown(markdown, origin) {
  if (!origin) return markdown
  const base = new URL(origin)
  if (!['https:', 'http:'].includes(base.protocol)) throw new Error('文档下载地址必须使用 HTTP 或 HTTPS')
  const source = String(markdown)
  const env = {}, tokens = locator.parse(source, env), replacements = new Map()
  const lines = [0]
  for (let i = 0; i < source.length; i++) if (source[i] === '\n') lines.push(i + 1)
  const masked = source.split('')
  const record = (start, end, href) => {
    if (!/^\/(?![\/\\])/.test(href) || !parser.validateLink(href)) return
    const angle = source[start] === '<'
    const raw = source.slice(start, end)
    replacements.set(start, { end, text: angle ? '<' + base.origin + raw.slice(1) : base.origin + raw })
  }
  for (const token of tokens) {
    if (!token.map || !['fence', 'code_block', 'reference_definition'].includes(token.type)) continue
    const start = lines[token.map[0]], end = lines[token.map[1]] ?? source.length
    if (token.type === 'reference_definition') {
      const match = source.slice(start, end).match(/(?<!\\)\]:\s*/)
      if (match) {
        const position = start + match.index + match[0].length
        const destination = locator.helpers.parseLinkDestination(source, position, end)
        if (destination.ok) record(position, destination.pos, destination.str)
      }
    }
    for (let i = start; i < end; i++) if (masked[i] !== '\n') masked[i] = ' '
  }
  const text = masked.join('')
  let sourceRange
  for (const token of tokens) {
    if (token.map) sourceRange = token.map
    // Table cells inherit their enclosing row's line map.
    if (token.type !== 'inline' || !sourceRange) continue
    const start = lines[sourceRange[0]], end = lines[sourceRange[1]] ?? source.length
    env.record = (from, to, href) => record(start + from, start + to, href)
    locator.inline.parse(text.slice(start, end), locator, env, [])
  }
  let result = source
  for (const [start, replacement] of [...replacements].sort((a, b) => b[0] - a[0])) {
    result = result.slice(0, start) + replacement.text + result.slice(replacement.end)
  }
  return result
}
