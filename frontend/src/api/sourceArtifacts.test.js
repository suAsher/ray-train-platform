import assert from 'node:assert/strict'
import test from 'node:test'

import { sha256File, validateSourceArchive } from './sourceArtifactFile.js'

const file = (name, bytes) => ({
  name,
  size: bytes.byteLength,
  arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
})

test('source archive validation accepts a bounded ZIP and hashes its exact bytes', async () => {
  const archive = file('bevfusion.zip', new TextEncoder().encode('abc'))

  validateSourceArchive(archive)
  assert.equal(await sha256File(archive), 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
})

test('source archive validation rejects non-ZIP and oversized input before upload', () => {
  assert.throws(() => validateSourceArchive(file('bevfusion.tar', new Uint8Array([1]))), /ZIP/)
  assert.throws(() => validateSourceArchive({ name: 'bevfusion.zip', size: 512 * 1024 * 1024 + 1 }), /512 MiB/)
})
