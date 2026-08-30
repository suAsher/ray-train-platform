import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import {
  datasetPath,
  datasetPublicationPath,
  datasetVersionPath,
  datasetVersionsPath,
} from './datasetPaths.js'

test('dataset API paths encode every opaque identifier exactly once', () => {
  assert.equal(datasetPath('dataset /中文?#'), '/api/v1/datasets/dataset%20%2F%E4%B8%AD%E6%96%87%3F%23')
  assert.equal(datasetVersionsPath('dataset/a'), '/api/v1/datasets/dataset%2Fa/versions')
  assert.equal(datasetVersionPath('dataset/a', 'version+b'), '/api/v1/datasets/dataset%2Fa/versions/version%2Bb')
  assert.equal(datasetPublicationPath('dataset/a'), '/api/v1/datasets/dataset%2Fa/publications')
})

test('dataset API wrapper stays same-origin and never accepts a storage URI', async () => {
  const source = (await Promise.all([
    readFile(new URL('./datasets.js', import.meta.url), 'utf8'),
    readFile(new URL('./datasetPaths.js', import.meta.url), 'utf8'),
  ])).join('\n')

  for (const endpoint of ['/api/v1/datasets', 'versions', 'publications', 'deprecate', 'gc/dry-run']) {
    assert.match(source, new RegExp(endpoint.replaceAll('/', '\\/')))
  }
  assert.doesNotMatch(source, /tos:\/\/|accessKey|secretKey|manifestObjectKey/)
})
