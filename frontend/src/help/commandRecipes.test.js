import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { helpSections, renderHelpMarkdown } from './content.js'
import { nativeStreamingMetadata } from './commandRecipes.js'

test('native metadata uses complete resource strings and JSON-encoded sites', () => {
  for (const key of ['image', 'worker-replicas', 'gpus-per-worker', 'cpu-per-worker', 'memory-per-worker', 'queue']) assert.ok(nativeStreamingMetadata[`ray-platform.${key}`])
  assert.ok(Object.values(nativeStreamingMetadata).every(value => typeof value === 'string'))
  assert.equal(nativeStreamingMetadata['platform.training.engine'], 'ray-train')
  assert.ok(Array.isArray(JSON.parse(nativeStreamingMetadata['platform.dataset.sites'])))
})

test('CLI recipes cover catalog, tagged images, datasets, native metadata and follow-up commands', () => {
  const topic = helpSections.find(s => s.id === 'command-recipes')
  assert.ok(topic, 'missing command-first guide')
  const text = renderHelpMarkdown([topic])
  for (const term of ['spk-rayjob images', '--output json', '--image', '--dataset-sites', '--metadata-json', 'platform.dataset.sites', 'ray job status', 'ray job logs', 'ray job stop', '--resume-from-job']) assert.ok(text.includes(term), term)
  for (const block of topic.blocks.filter(b => b.lang === 'bash')) {
    for (const shell of ['/bin/bash', '/bin/zsh']) {
      const result = spawnSync(shell, ['-n'], { input: block.text, encoding: 'utf8' })
      assert.equal(result.status, 0, result.stderr)
    }
  }
})
