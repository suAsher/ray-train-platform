import test from 'node:test'
import assert from 'node:assert/strict'
import { normalizeCLIRelease, fetchCLIRelease } from './cliRelease.js'

const release = { schemaVersion: 1, latestVersion: 'release-20260906-10', minimumVersion: 'release-20260906-2', releaseNotes: '更新提示与主动升级' }

test('CLI release display accepts numeric release ordering, not lexicographic', () => {
  assert.deepEqual(normalizeCLIRelease(release), release)
  assert.throws(() => normalizeCLIRelease({ ...release, minimumVersion: 'release-20260906-11' }))
  assert.throws(() => normalizeCLIRelease({ ...release, latestVersion: 'dev' }))
  assert.throws(() => normalizeCLIRelease({ ...release, latestVersion: 'release-20260230-01' }))
  assert.throws(() => normalizeCLIRelease({ ...release, schemaVersion: 2 }))
  assert.throws(() => normalizeCLIRelease({ ...release, releaseNotes: 'x'.repeat(8001) }))
})

test('CLI metadata requests do not carry credentials and reject oversized/offline responses', async () => {
  const fetcher = async (path, options) => {
    assert.equal(path, '/downloads/spk-rayjob/release.json')
    assert.equal(options.credentials, 'omit')
    assert.equal(options.redirect, 'error')
    return new Response(JSON.stringify(release))
  }
  assert.deepEqual(await fetchCLIRelease(fetcher), release)
  await assert.rejects(fetchCLIRelease(async () => new Response('offline', { status: 503 })))
  await assert.rejects(fetchCLIRelease(async () => new Response('x'.repeat(65537))))
  assert.equal(normalizeCLIRelease({ ...release, minimumVersion: '' }).minimumVersion, '')
})
