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

test('node onboarding distinguishes scheduling from NFS mount and image readiness', () => {
  const text = renderHelpSectionMarkdown(helpSections.find(item => item.id === 'cli-onboarding-v2'))
  for (const expected of ['apt-get install --no-install-recommends nfs-common', 'command -v mount.nfs', '无需重启内核或 kubelet', '标签不代表存储就绪', 'Scheduled', 'FailedMount', 'ImagePullBackOff', '未生产启用并验收前', '不会自动接入']) assert.ok(text.includes(expected), expected)
})

test('node onboarding documents the gated automatic flow without claiming deployment', () => {
  const text = renderHelpSectionMarkdown(helpSections.find(item => item.id === 'cli-onboarding-v2'))
  for (const expected of ['ray-node-onboarding controller', 'ResourceFlavor', 'cache-ready gate', 'root:root', '0755', '不要手工设置 platform.wellspiking.ai/cache-ready', '节点需要非 cordon', '定向 PVC 探针', '平台 ready 前训练必须被 cache-ready gate 挡住', '超级管理员再分配团队配额', '未启用这些门禁时继续走人工注册流程', '已有运行任务不会重启']) assert.ok(text.includes(expected), expected)
})
