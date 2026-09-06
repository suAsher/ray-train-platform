import test from 'node:test'
import assert from 'node:assert/strict'
import { helpSections, renderHelpSectionMarkdown } from './content.js'

test('new editable onboarding topic covers both clients and version boundaries', () => {
  const topic = helpSections.find(item => item.id === 'cli-onboarding-v2')
  assert.ok(topic)
  const text = renderHelpSectionMarkdown(topic)
  for (const expected of ['spk-rayjob upgrade', 'stderr', 'SHA256', 'Windows', 'macOS', 'Linux', '2.35.0', '2.58', 'ray job submit', 'ray job stop', '--resume-from-job', 'platform.dataset.sites', 'queue', '--entrypoint']) assert.ok(text.includes(expected), expected)
})

test('administrator onboarding preserves workloads and explains irreversible retirement', () => {
  const text = renderHelpSectionMarkdown(helpSections.find(item => item.id === 'cli-onboarding-v2'))
  for (const expected of ['kubectl cordon', 'register-node.sh', 'data1-values-patch.yaml', 'data2-values-patch.yaml', '--reuse-values', 'kubectl uncordon', '超级管理员', '活动', '保留', '恢复']) assert.ok(text.includes(expected), expected)
})
