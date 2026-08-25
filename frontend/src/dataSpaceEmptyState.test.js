import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const dataPage = new URL('./views/DataCache/index.vue', import.meta.url)
const devcenterPage = new URL('./views/Devcenter/index.vue', import.meta.url)

test('empty personal files explains its exact workload path and where checkpoints appear', async () => {
  const source = await readFile(dataPage, 'utf8')

  assert.match(source, /我的文件只显示 \/mnt\/storage\/me\/files/)
  assert.match(source, /训练权重和任务输出请到“我的运行结果”查看/)
})

test('personal storage is one directly browsable root instead of nested navigation cards', async () => {
  const source = await readFile(dataPage, 'utf8')

  assert.match(source, /v-for="space in visibleSpaces"/)
  assert.match(source, /dataPageSpaces/)
  assert.doesNotMatch(source, /selectSpaceByID/)
  assert.doesNotMatch(source, /进入目录 →/)
})

test('debug workspace points personal files at the direct governed root', async () => {
  const source = await readFile(devcenterPage, 'utf8')

  assert.match(source, /个人文件:.*\/mnt\/storage\/me/)
  assert.doesNotMatch(source, /个人文件:.*\/mnt\/storage\/me\/files/)
  assert.match(source, /训练结果:.*\/mnt\/storage\/me\/runs/)
})
