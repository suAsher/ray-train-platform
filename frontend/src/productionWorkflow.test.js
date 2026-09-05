import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8')

test('production workflow guides users from durable workspace to a reproducible training job', async () => {
  const [dataCacheSource, stepCodeSource, artifactBrowserSource, externalSubmitSource, accountSecuritySource, routerSource, layoutSource] = await Promise.all([
    read('./views/DataCache/index.vue'),
    read('./components/job/StepCode.vue'),
    read('./components/JobArtifactBrowser.vue'),
    read('./views/ExternalSubmit/index.vue'),
    read('./views/AccountSecurity/index.vue'),
    read('./router/index.js'),
    read('./layout/Layout.vue'),
  ])

  assert.match(dataCacheSource, /在“我的工作区”上传或同步代码/)
  assert.match(dataCacheSource, /创建不可变代码版本/)
  assert.match(dataCacheSource, /PLATFORM_DATASET_PATH/)
  assert.match(stepCodeSource, /调试快照/)
  assert.equal(artifactBrowserSource.includes('请下载后查看'), false)
  assert.match(artifactBrowserSource, /调试环境的个人运行目录/)
  assert.match(externalSubmitSource, /\/downloads\/spk-rayjob\/spk-rayjob-linux-amd64/)
  assert.match(externalSubmitSource, /spk-rayjob login/)
  assert.match(externalSubmitSource, /commands.value.login/)
  assert.match(await read('./help/externalSubmit.js'), /--password-stdin/)
  assert.match(externalSubmitSource, /与网页登录同一个账号/)
  assert.match(externalSubmitSource, /spk-rayjob submit/)
  assert.match(externalSubmitSource, /方式一：spk-rayjob/)
  assert.match(externalSubmitSource, /方式二：原生 Ray CLI/)
  assert.match(externalSubmitSource, /ray job submit/)
  assert.equal(externalSubmitSource.includes('kubeconfig'), false)
  assert.equal(externalSubmitSource.includes('TOS AK\/SK'), false)
  assert.equal(externalSubmitSource.includes('不需要 Kubernetes 权限'), false)
  assert.match(accountSecuritySource, /个人访问令牌/)
  assert.match(accountSecuritySource, /createPersonalAccessToken/)
  assert.match(routerSource, /ExternalSubmit/)
  assert.match(layoutSource, /外部提交/)
})

// Comments may discuss the sizes that were removed; only what a user reads on
// screen matters here.
const withoutComments = (source) => source
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/^\s*\/\/.*$/gm, '')

// Cluster sizes are a deployment fact. The Portal used to print a fixed fleet
// size in its copy, which drifted from the real cluster and let users compose
// jobs the server then rejected. Sizes must come from the API.
test('no view states a fixed cluster size in its copy', async () => {
  const views = await Promise.all([
    read('./views/Job/CreateJob.vue'),
    read('./views/Job/JobDetail.vue'),
    read('./views/QuotaManage/index.vue'),
    read('./components/job/StepRuntime.vue'),
  ])
  for (const source of views.map(withoutComments)) {
    assert.equal(/\d+\s*卡(总容量|生产资源池)/.test(source), false)
    assert.equal(/多节点\s*\d+卡/.test(source), false)
    assert.equal(/\d+\s*卡\s*RTX/.test(source), false)
  }
})

test('the submit form derives GPU ceilings from the platform limits endpoint', async () => {
  const [formSource, runtimeSource] = await Promise.all([
    read('./composables/useJobForm.js'),
    read('./components/job/StepRuntime.vue'),
  ])
  assert.match(formSource, /fetchPlatformLimits/)
  assert.match(runtimeSource, /limits\.maxGpusPerWorker/)
  assert.match(runtimeSource, /limits\.maxWorkerReplicas/)
})
