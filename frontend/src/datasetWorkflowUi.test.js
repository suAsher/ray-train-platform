import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8')

test('dataset navigation and pages are capability-gated and never expose storage internals', async () => {
  const [layout, router, page, picker] = await Promise.all([
    read('./layout/Layout.vue'),
    read('./router/index.js'),
    read('./views/Datasets/index.vue'),
    read('./components/DatasetVersionPicker.vue'),
  ])

  assert.match(layout, /datasetCatalogEnabled/)
  assert.match(layout, /数据集/)
  assert.match(router, /path:\s*'datasets'/)
  assert.match(page, /版本化数据集/)
  assert.match(page, /最新 READY 版本/)
  assert.match(picker, /最新可用/)
  assert.match(picker, /train.*val.*test/is)
  for (const source of [page, picker]) {
    assert.doesNotMatch(source, /TOS URI|AK\/SK|PVC|manifestObjectKey|accessKey|secretKey/)
  }
})

test('job form preserves legacy data spaces and adds governed streaming plus preflight', async () => {
  const [create, data, runtime, preview] = await Promise.all([
    read('./views/Job/CreateJob.vue'),
    read('./components/job/StepData.vue'),
    read('./components/job/StepRuntime.vue'),
    read('./components/job/SubmitPreview.vue'),
  ])

  assert.match(data, /DatasetVersionPicker/)
  assert.match(data, /DataSpacePicker/)
  assert.match(runtime, /版本化流式/)
  assert.match(runtime, /dataModeClass\('streaming'\)/)
  assert.match(create, /\/api\/v1\/jobs\/preflight/)
  assert.match(create, /pinStreamingPreflight/)
  assert.match(preview, /固定数据版本/)
  assert.match(preview, /提交前检查/)
})

test('administrator console exposes governed publishing while flags off preserve the existing console', async () => {
  const [consoleSource, panel] = await Promise.all([
    read('./views/QuotaManage/index.vue'),
    read('./components/admin/DatasetGovernancePanel.vue'),
  ])

  assert.match(consoleSource, /DatasetGovernancePanel/)
  assert.match(consoleSource, /datasetCapabilities\.catalogEnabled/)
  assert.match(panel, /创建数据集/)
  assert.match(panel, /发布新版本/)
  assert.match(panel, /弃用版本/)
  assert.match(panel, /回收预览/)
  assert.match(panel, /publisherEnabled/)
	assert.match(panel, /发布进度/)
	assert.match(panel, /fetchDatasetPublication/)
	assert.doesNotMatch(panel, /sourceRoot|objectKey|credential|jobName/)
})
