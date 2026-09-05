import assert from 'node:assert/strict'
import test from 'node:test'

import {
  assertStreamingPreflightCurrent,
  datasetVersionDelta,
  datasetVersionOptions,
  formatDatasetBytes,
  normalizeDatasetCapabilities,
  normalizeDatasetList,
  normalizeDatasetVersions,
  pinStreamingPreflight,
  preflightFingerprint,
  streamingDatasetAvailability,
} from './datasetCatalog.js'

const digest = 'a'.repeat(64)
const imageReference = `registry.example/s1h@sha256:${digest}`

test('dataset capabilities fail closed and never retain caller-owned arrays or objects', () => {
  const input = {
    versioningEnabled: true,
    streamingEnabled: true,
    publisherEnabled: true,
  }
  const normalized = normalizeDatasetCapabilities(input)

  assert.deepEqual(normalized, {
    versioningEnabled: true,
    streamingEnabled: true,
    publisherEnabled: true,
    catalogEnabled: true,
  })
  assert.notEqual(normalized, input)
  assert.deepEqual(normalizeDatasetCapabilities({ streamingEnabled: true }), {
    versioningEnabled: false,
    streamingEnabled: false,
    publisherEnabled: false,
    catalogEnabled: false,
  })
})

test('streaming availability requires the caller flag, Ray Train canary, and a compatible selected image', () => {
  const limits = {
    datasets: { versioningEnabled: true, streamingEnabled: true },
    runtime: {
      managedEnabled: true,
      canaryEnabled: true,
      availableEngines: ['ray-ddp', 'ray-train'],
      canaryRayVersion: '2.58.0',
    },
  }
  const images = [{ reference: imageReference, rayVersion: '2.58.0', supportedEngines: ['ray-train'] }]

  assert.deepEqual(streamingDatasetAvailability({ limits, images, imageReference }), {
    available: true,
    reason: '',
    rayVersion: '2.58.0',
  })
  assert.match(streamingDatasetAvailability({ limits: {}, images, imageReference }).reason, /未开放/)
  assert.match(streamingDatasetAvailability({
    limits,
    images: [{ ...images[0], rayVersion: '2.56.1' }],
    imageReference,
  }).reason, /2\.58\.0/)
})

test('catalog normalizers expose only logical metadata and own fresh nested-free records', () => {
  const datasets = normalizeDatasetList([{
    id: 'dataset-a', slug: 'labeled-full', name: '全量标注', description: '生产数据',
    sourceSpace: 'public', sourceRelativePath: 'labeled', visibility: 'PUBLIC', schemaVersion: 's1h-v1',
    manifestObjectKey: 'ray-train/platform/datasets/private', accessKey: 'secret',
  }])
  const versions = normalizeDatasetVersions([{
    id: 'version-a', datasetId: 'dataset-a', version: '2026.08.30', state: 'READY', manifestSha256: digest,
    schemaVersion: 's1h-v1', trainSamples: 15228, valSamples: 1620, testSamples: 0,
    sourceObjectCount: 91216, logicalBytes: 10 * 1024 ** 3, packedBytes: 8 * 1024 ** 3,
    manifestObjectKey: 'ray-train/platform/datasets/private',
  }])

  assert.equal(datasets[0].manifestObjectKey, undefined)
  assert.equal(datasets[0].accessKey, undefined)
  assert.equal(versions[0].manifestObjectKey, undefined)
  assert.equal(versions[0].trainSamples, 15228)
  assert.notEqual(datasets[0], datasets)
})

test('version options offer latest as a selector but only READY versions as immutable choices', () => {
  const versions = normalizeDatasetVersions([
    { id: 'version-new', datasetId: 'dataset-a', version: '2026.08.31', state: 'PACKING' },
    { id: 'version-ready-2', datasetId: 'dataset-a', version: '2026.08.30.2', state: 'READY', manifestSha256: digest, trainSamples: 20, packedBytes: 100 },
    { id: 'version-ready-1', datasetId: 'dataset-a', version: '2026.08.30.1', state: 'READY', manifestSha256: 'b'.repeat(64), trainSamples: 10, packedBytes: 50 },
    { id: 'version-old', datasetId: 'dataset-a', version: '2026.08.29', state: 'DEPRECATED', manifestSha256: 'c'.repeat(64) },
  ])

  assert.deepEqual(datasetVersionOptions(versions).map(({ value, resolvedVersionId }) => ({ value, resolvedVersionId })), [
    { value: 'latest', resolvedVersionId: 'version-ready-2' },
    { value: 'version-ready-2', resolvedVersionId: 'version-ready-2' },
    { value: 'version-ready-1', resolvedVersionId: 'version-ready-1' },
  ])
})

test('version deltas and byte formatting stay explicit for zero, growth, and shrinkage', () => {
  assert.deepEqual(datasetVersionDelta(
    { trainSamples: 120, valSamples: 12, testSamples: 1, sourceObjectCount: 300, logicalBytes: 2048, packedBytes: 1024 },
    { trainSamples: 100, valSamples: 10, testSamples: 1, sourceObjectCount: 250, logicalBytes: 1024, packedBytes: 2048 },
  ), {
    trainSamples: 20,
    valSamples: 2,
    testSamples: 0,
    sourceObjectCount: 50,
    logicalBytes: 1024,
    packedBytes: -1024,
  })
  assert.equal(formatDatasetBytes(0), '0 B')
  assert.equal(formatDatasetBytes(1024), '1 KiB')
  assert.equal(formatDatasetBytes(null), '—')
})

test('preflight pins latest immutably and rejects incomplete or inconsistent server results', () => {
  const spec = {
    image: imageReference,
    trainingEngine: 'ray-train',
    dataMode: 'streaming',
    resources: { workerReplicas: 2, gpusPerWorker: 8 },
    datasetRef: { dataset: 'labeled-full', version: 'latest' },
    cachePolicy: 'auto',
  }
  const result = {
    image: imageReference,
    trainingEngine: 'ray-train',
    rayVersion: '2.58.0',
    requestedGpus: 16,
    dataset: {
      datasetId: 'dataset-a', datasetSlug: 'labeled-full', versionId: 'version-ready-2', version: '2026.08.30.2',
      manifestSha256: digest, schemaVersion: 's1h-v1', trainSamples: 15228, valSamples: 1620, testSamples: 0,
      logicalBytes: 1024, packedBytes: 512, dataMode: 'streaming', cachePolicy: 'auto',
    },
  }
  const original = structuredClone(spec)
  const pinned = pinStreamingPreflight(spec, result, '2.58.0')

  assert.deepEqual(pinned.datasetRef, { dataset: 'dataset-a', version: 'version-ready-2' })
  const scopedSpec = { ...spec, datasetRef: { ...spec.datasetRef, sites: ['site-b', 'site-a', 'site-a'] } }
  const scopedResult = { ...result, dataset: { ...result.dataset, sites: ['site-a', 'site-b'], selectionValidation: 'pending-manifest-validation' } }
  assert.deepEqual(pinStreamingPreflight(scopedSpec, scopedResult, '2.58.0').datasetRef.sites, ['site-a', 'site-b'])
  assert.throws(() => pinStreamingPreflight(scopedSpec, result, '2.58.0'), /场地范围/)
  assert.throws(() => pinStreamingPreflight({ ...scopedSpec, datasetRef: { ...scopedSpec.datasetRef, sites: ['../site'] } }, scopedResult, '2.58.0'), /场地编码/)
  assert.deepEqual(spec, original)
  assert.notEqual(pinned, spec)
  assert.notEqual(
    preflightFingerprint({ ...spec, entrypoint: { command: ['python'], args: ['train.py'] } }),
    preflightFingerprint({ ...spec, entrypoint: { command: ['python'], args: ['train.py', '--debug'] } }),
  )
  assert.doesNotThrow(() => assertStreamingPreflightCurrent(spec, structuredClone(spec)))
  assert.throws(
    () => assertStreamingPreflightCurrent(spec, { ...spec, entrypoint: { command: ['python'], args: ['train.py', '--debug'] } }),
    /发生变化/,
  )
  const future = pinStreamingPreflight(spec, { ...result, rayVersion: '2.59.0' }, '2.59.0')
  assert.deepEqual(future.datasetRef, pinned.datasetRef)
  assert.throws(() => pinStreamingPreflight(spec, result, '2.59.0'), /运行环境/)
  assert.throws(() => pinStreamingPreflight(spec, result, ''), /运行环境/)
  assert.throws(() => pinStreamingPreflight(spec, { ...result, requestedGpus: 8 }, '2.58.0'), /资源/)
  assert.throws(() => pinStreamingPreflight(spec, { ...result, dataset: { ...result.dataset, manifestSha256: 'bad' } }, '2.58.0'), /数据版本/)
  assert.throws(() => pinStreamingPreflight({ ...spec, dataMode: 'mount' }, result, '2.58.0'), /流式/)
})
