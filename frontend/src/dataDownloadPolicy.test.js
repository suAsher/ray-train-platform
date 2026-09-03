import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const dataPage = new URL('./views/DataCache/index.vue', import.meta.url)
const artifactBrowser = new URL('./components/JobArtifactBrowser.vue', import.meta.url)
const experimentPage = new URL('./views/Experiments/index.vue', import.meta.url)
const downloadPolicy = new URL('./checkpointDownload.js', import.meta.url)

test('governed data browsers do not render download actions', async () => {
  const experimentPageSource = await readFile(experimentPage, 'utf8')

  // The separate native MLflow surface intentionally supports Artifact upload
  // and download. It must not add a direct governed training-data download path
  // to the filtered Experiment Center itself.
  assert.equal(experimentPageSource.includes('downloadJobArtifact'), false)
  assert.equal(experimentPageSource.includes('/api/v1/data-spaces'), false)
  assert.equal(experimentPageSource.includes('/api/v1/jobs/') && experimentPageSource.includes('/artifacts'), false)
  assert.match(experimentPageSource, /不会开放公开训练数据下载/)
})

// Users own the weights they trained and need them off-platform, so both file
// browsers offer a download. The action stays narrower than the browser itself:
// only checkpoints, and in the data page only spaces the caller can write.
// Governed team/public roots are read-only there and stay closed.
test('download actions are limited to checkpoints the caller owns', async () => {
  const [dataPageSource, artifactBrowserSource, policySource] = await Promise.all([
    readFile(dataPage, 'utf8'),
    readFile(artifactBrowser, 'utf8'),
    readFile(downloadPolicy, 'utf8')
  ])

  // The allowlist is shared so the two browsers cannot drift from each other or
  // from the server-side policy that ultimately rejects anything else.
  assert.match(policySource, /CHECKPOINT_EXTENSIONS = \['\.pth', '\.pt', '\.ckpt', '\.onnx', '\.safetensors'\]/)

  assert.match(artifactBrowserSource, /downloadJobArtifact/)
  assert.match(artifactBrowserSource, /v-if="isCheckpointFile\(entry\.name\)"/)

  assert.match(dataPageSource, /downloadDataSpaceFile/)
  assert.match(dataPageSource, /v-if="canDownloadEntry\(entry\)"/)
  assert.match(dataPageSource, /canMutate\.value && isCheckpointFile\(entry\.name\)/)
})

test('training artifact browser explains checkpoint reuse and the MLflow boundary', async () => {
  const source = await readFile(artifactBrowser, 'utf8')

  assert.match(source, /权重不会自动复制到 MLflow/)
  assert.match(source, /\/mnt\/storage\/me\/runs/)
  assert.match(source, /续训/)
})
