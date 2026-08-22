import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const devcenter = new URL('./views/Devcenter/index.vue', import.meta.url)

test('dev workspace shows the real governed-storage state instead of claiming mounts are disabled', async () => {
  const source = await readFile(devcenter, 'utf8')

  assert.equal(source.includes('GPU 存储挂载尚未启用'), false)
  assert.match(source, /受控数据目录正在准备中/)
  assert.match(source, /受控数据目录挂载失败/)
})
