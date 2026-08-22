import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const dataPage = new URL('./views/DataCache/index.vue', import.meta.url)
const artifactBrowser = new URL('./components/JobArtifactBrowser.vue', import.meta.url)
const experimentPage = new URL('./views/Experiments/index.vue', import.meta.url)

test('data, training artifact, and experiment browsers do not render download actions', async () => {
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
  assert.equal(experimentPageSource.toLowerCase().includes('artifact'), false)
  assert.equal(experimentPageSource.toLowerCase().includes('download'), false)
  assert.equal(experimentPageSource.includes('下载'), false)
})
