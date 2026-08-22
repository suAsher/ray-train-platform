import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const dataPage = new URL('./views/DataCache/index.vue', import.meta.url)
const artifactBrowser = new URL('./components/JobArtifactBrowser.vue', import.meta.url)
const experimentPage = new URL('./views/Experiments/index.vue', import.meta.url)

test('governed data and training artifact browsers do not render download actions', async () => {
  const [dataPageSource, artifactBrowserSource, experimentPageSource] = await Promise.all([
    readFile(dataPage, 'utf8'),
    readFile(artifactBrowser, 'utf8'),
    readFile(experimentPage, 'utf8')
  ])

  assert.equal(dataPageSource.includes('downloadFile('), false)
  assert.equal(dataPageSource.includes('>下载<'), false)
  assert.equal(artifactBrowserSource.includes('downloadJobArtifact'), false)
  assert.equal(artifactBrowserSource.includes('@click="download('), false)
  assert.equal(artifactBrowserSource.includes('请下载后查看'), false)

  // The separate native MLflow surface intentionally supports Artifact upload
  // and download. It must not add a direct governed training-data download path
  // to the filtered Experiment Center itself.
  assert.equal(experimentPageSource.includes('downloadJobArtifact'), false)
  assert.equal(experimentPageSource.includes('/api/v1/data-spaces'), false)
  assert.equal(experimentPageSource.includes('/api/v1/jobs/') && experimentPageSource.includes('/artifacts'), false)
  assert.match(experimentPageSource, /不会开放公开训练数据下载/)
})
