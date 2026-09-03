import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const dataPage = new URL('./views/DataCache/index.vue', import.meta.url)
const artifactBrowser = new URL('./components/JobArtifactBrowser.vue', import.meta.url)
const experimentPage = new URL('./views/Experiments/index.vue', import.meta.url)

test('governed data browsers do not render download actions', async () => {
  const [dataPageSource, experimentPageSource] = await Promise.all([
    readFile(dataPage, 'utf8'),
    readFile(experimentPage, 'utf8')
  ])

  assert.equal(dataPageSource.includes('downloadFile('), false)
  assert.equal(dataPageSource.includes('>下载<'), false)

  // The separate native MLflow surface intentionally supports Artifact upload
  // and download. It must not add a direct governed training-data download path
  // to the filtered Experiment Center itself.
  assert.equal(experimentPageSource.includes('downloadJobArtifact'), false)
  assert.equal(experimentPageSource.includes('/api/v1/data-spaces'), false)
  assert.equal(experimentPageSource.includes('/api/v1/jobs/') && experimentPageSource.includes('/artifacts'), false)
  assert.match(experimentPageSource, /不会开放公开训练数据下载/)
})

// Users own the weights they trained and need them off-platform, so the artifact
// browser offers a download. It stays narrower than the browser itself: only
// checkpoint files get the action, matching the server-side download policy, so
// opening this path never turns into a general file export.
test('training artifact browser downloads checkpoints only', async () => {
  const source = await readFile(artifactBrowser, 'utf8')

  assert.match(source, /downloadJobArtifact/)
  assert.match(source, /v-if="isCheckpoint\(entry\.name\)"/)
  assert.match(source, /CHECKPOINT_EXTENSIONS = \['\.pth', '\.pt', '\.ckpt', '\.onnx', '\.safetensors'\]/)
})

test('training artifact browser explains checkpoint reuse and the MLflow boundary', async () => {
  const source = await readFile(artifactBrowser, 'utf8')

  assert.match(source, /权重不会自动复制到 MLflow/)
  assert.match(source, /\/mnt\/storage\/me\/runs/)
  assert.match(source, /续训/)
})
