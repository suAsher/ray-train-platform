import test from 'node:test'
import assert from 'node:assert/strict'
import { helpSections, renderHelpSectionMarkdown } from './content.js'

test('standalone CLI onboarding distinguishes server, binary and optional storage', () => {
  const text = renderHelpSectionMarkdown(helpSections.find(item => item.id === 'cli-onboarding-v2')))
  for (const expected of ['raytrain.example.com', 'SHA256', 'type -a spk-rayjob', 'Get-Command spk-rayjob -All', 'PVC', 'working-dir', 'Scheduled', 'FailedMount', 'ImagePullBackOff']) assert.ok(text.includes(expected), expected)
  for (const forbidden of ['wellspiking', 'IDC', 'register-node.sh']) assert.ok(!text.includes(forbidden), forbidden)
})
