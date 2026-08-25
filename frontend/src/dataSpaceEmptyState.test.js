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

test('personal storage overview exposes the pod root and navigates to governed child spaces', async () => {
  const source = await readFile(dataPage, 'utf8')

  assert.match(source, /个人空间/)
  assert.match(source, /\/mnt\/storage\/me/)
  assert.match(source, /\/mnt\/storage\/me\/files/)
  assert.match(source, /\/mnt\/storage\/me\/runs/)
  assert.match(source, /selectSpaceByID\('my-files'\)/)
  assert.match(source, /selectSpaceByID\('my-runs'\)/)
})

test('debug workspace points personal files at the governed files subdirectory', async () => {
  const source = await readFile(devcenterPage, 'utf8')

  assert.match(source, /个人文件:.*\/mnt\/storage\/me\/files/)
  assert.match(source, /训练结果:.*\/mnt\/storage\/me\/runs/)
})
