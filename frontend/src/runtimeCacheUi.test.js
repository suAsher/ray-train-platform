import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8')

test('job form defaults cache off and renders only the server-enabled runtime choices', async () => {
  const [formSource, runtimeSource] = await Promise.all([
    read('./composables/useJobForm.js'),
    read('./components/job/StepRuntime.vue'),
  ])

  assert.match(formSource, /cacheMode:\s*'off'/)
  assert.match(formSource, /cacheSize:\s*''/)
  assert.match(runtimeSource, /v-if="cachePolicy\.enabled"/)
  assert.match(runtimeSource, /allowedSizes/)
  assert.match(runtimeSource, /value="off"/)
  assert.match(runtimeSource, /value="runtime"/)
})

test('runtime cache warning describes its disposable and explicit-only boundary in Chinese', async () => {
  const runtimeSource = await read('./components/job/StepRuntime.vue')

  assert.match(runtimeSource, /随任务结束释放的一次性缓存/)
  assert.match(runtimeSource, /Ray 临时文件/)
  assert.match(runtimeSource, /object spill/)
  assert.match(runtimeSource, /显式写入[\s\S]*cachePolicy\.mountPath/)
  assert.doesNotMatch(runtimeSource, /<code>\/mnt\/cache<\/code>/)
  assert.match(runtimeSource, /不会自动缓存.*\/mnt\/storage.*公共数据.*DataLoader/)
  assert.match(runtimeSource, /输出和 Checkpoint.*持久存储/)
})

test('submit preview visibly states cache off or runtime size and mount path', async () => {
  const [previewSource, createSource] = await Promise.all([
    read('./components/job/SubmitPreview.vue'),
    read('./views/Job/CreateJob.vue'),
  ])

  assert.match(previewSource, />运行时缓存</)
  assert.match(previewSource, /cacheSummary/)
  assert.match(previewSource, /cachePolicy\.mountPath/)
  assert.match(previewSource, /已关闭/)
  assert.match(createSource, /<SubmitPreview[\s\S]*?:cache-policy="limits\.cache"[\s\S]*?\/>/)
})

test('copy and resubmit carry cache query values while the form validates them after limits load', async () => {
  const [listSource, detailSource, formSource] = await Promise.all([
    read('./views/Job/index.vue'),
    read('./views/Job/JobDetail.vue'),
    read('./composables/useJobForm.js'),
  ])

  for (const source of [listSource, detailSource]) {
    assert.match(source, /cacheMode/)
    assert.match(source, /cacheSize/)
  }
  assert.match(formSource, /route\?\.query\?\.cacheMode/)
  assert.match(formSource, /route\?\.query\?\.cacheSize/)
  assert.match(formSource, /normalizeCacheSelection/)
  assert.ok(
    formSource.indexOf('await Promise.allSettled') < formSource.lastIndexOf('route?.query?.cacheMode'),
    'cache query must be applied only after the limits request settles',
  )
})
