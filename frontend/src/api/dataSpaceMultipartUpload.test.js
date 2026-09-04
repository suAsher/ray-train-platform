import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { multipartPartBounds, uploadMultipartDataSpaceFile } from './dataSpaceMultipartUpload.js'

const digest = (value) => createHash('sha256').update(value).digest('hex')

function fakeFile(value) {
  const blob = new Blob([value])
  return { name: 'model.pth', size: blob.size, lastModified: 1, slice: (...args) => blob.slice(...args) }
}

test('multipartPartBounds preserves the short final part', () => {
  const ticket = { sizeBytes: 10, partSizeBytes: 4, totalParts: 3 }
  assert.deepEqual(multipartPartBounds(ticket, 1), { start: 0, end: 4, size: 4 })
  assert.deepEqual(multipartPartBounds(ticket, 3), { start: 8, end: 10, size: 2 })
  assert.throws(() => multipartPartBounds(ticket, 4), /分片计划/)
})

test('multipart upload resumes verified parts, limits concurrency, retries, and completes', async () => {
  const file = fakeFile('abcdefghij')
  const ticket = {
    mode: 'multipart', spaceId: 'my-files', sessionId: 'upload-1', sizeBytes: 10,
    partSizeBytes: 4, totalParts: 3,
    completedParts: [{ partNumber: 1, sizeBytes: 4, sha256: digest('abcd') }],
  }
  let active = 0
  let peak = 0
  let completions = 0
  const attempts = new Map()
  const progress = []
  await uploadMultipartDataSpaceFile(ticket, file, {
    concurrency: 3,
    hashBlob: async (blob) => digest(await blob.text()),
    uploadPart: async (_ticket, partNumber, blob, _digest, options) => {
      active += 1
      peak = Math.max(peak, active)
      attempts.set(partNumber, (attempts.get(partNumber) || 0) + 1)
      options.onProgress(blob.size)
      await new Promise((resolve) => setTimeout(resolve, 2))
      active -= 1
      if (partNumber === 2 && attempts.get(partNumber) === 1) {
        const error = new Error('temporary')
        error.status = 503
        throw error
      }
    },
    sleep: async () => {},
    completeUpload: async () => { completions += 1 },
    onProgress: (event) => progress.push(event.loaded),
  })

  assert.equal(attempts.has(1), false, 'verified part should not upload again')
  assert.equal(attempts.get(2), 2)
  assert.equal(attempts.get(3), 1)
  assert.ok(peak <= 3)
  assert.equal(completions, 1)
  assert.equal(progress.at(-1), 10)
})

test('multipart upload does not retry authorization failures', async () => {
  const file = fakeFile('abcd')
  let attempts = 0
  await assert.rejects(uploadMultipartDataSpaceFile({ mode: 'multipart', spaceId: 'my-files', sessionId: 'upload-2', sizeBytes: 4, partSizeBytes: 4, totalParts: 1 }, file, {
    hashBlob: async () => digest('abcd'),
    uploadPart: async () => {
      attempts += 1
      const error = new Error('forbidden')
      error.status = 403
      throw error
    },
    sleep: async () => {},
    completeUpload: async () => assert.fail('must not complete'),
  }), /forbidden/)
  assert.equal(attempts, 1)
})
