import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Portal shows the approved product name without a subtitle', async () => {
  const files = await Promise.all([
    readFile(new URL('./views/Login/index.vue', import.meta.url), 'utf8'),
    readFile(new URL('./layout/Layout.vue', import.meta.url), 'utf8')
  ])

  for (const source of files) {
    assert.match(source, />Ray Training Platform</)
    assert.doesNotMatch(source, /Ray AI Platform|分布式训练控制台|多租户分布式训练控制台/)
  }
})
