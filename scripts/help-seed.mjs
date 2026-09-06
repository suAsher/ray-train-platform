// Explicit bootstrap snapshot only: deployments never overwrite edited rows.
// Usage: node scripts/help-seed.mjs [--patch]
import { helpSections, renderHelpSectionMarkdown } from '../frontend/src/help/content.js'

const json = JSON.stringify(helpSections.map((section, index) => ({
  id: section.id,
  title: section.title,
  category: section.group || '其他',
  sortOrder: index * 10,
  markdown: renderHelpSectionMarkdown(section),
})), null, 2) + '\n'

if (process.argv.includes('--patch')) {
  process.stdout.write('*** Begin Patch\n*** Add File: backend/helpdocs/seed.json\n' + json.trimEnd().split('\n').map(line => '+' + line).join('\n') + '\n*** End Patch\n')
} else {
  process.stdout.write(json)
}
