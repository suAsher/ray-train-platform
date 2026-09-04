import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8')

// A browser sits outside the cluster. Object storage resolves to a VPC-internal
// address, so any upload the browser is told to make directly to it silently
// fails - and for source archives it also leaves a PENDING row that counts
// against the user's quota forever. Code archives must therefore travel through
// the platform, which is the only host a user is expected to reach.
test('code archive uploads go through the platform, not to object storage', async () => {
  const source = await read('./api/sourceArtifacts.js')

  assert.match(source, /\/ray\/api\/packages\/gcs\//, 'the archive is not sent to the platform relay')
  assert.equal(source.includes('uploadUrl'), false, 'a presigned object-store URL is still being used')
  assert.equal(source.includes('requiredHeaders'), false, 'object-store upload headers are still being applied')
  // The relay is an authenticated platform endpoint, unlike a presigned URL.
  assert.match(source, /Authorization/, 'the relay upload carries no credentials')
  // The package name is the archive digest, which the platform recomputes, so a
  // corrupted upload cannot be stored under a healthy name.
  assert.match(source, /sha256File/)
  assert.match(source, /\$\{sha256\}\.zip/)
})

// The response header is the only thing binding an upload to the artifact the
// job will reference. Continuing without it would submit a job against nothing.
test('the archive upload fails loudly when the platform returns no artifact id', async () => {
  const source = await read('./api/sourceArtifacts.js')

  assert.match(source, /X-Ray-Platform-Source-Artifact-ID/)
  assert.match(source, /if \(!artifactId\)/)
  assert.match(source, /throw new Error/)
})

// Presigned object-store URLs must never receive platform credentials, so the
// two kinds of header stay separate rather than being merged by the transport.
test('the upload transport keeps store headers and platform headers apart', async () => {
  const source = await read('./api/dataSpaceUpload.js')

  assert.match(source, /upload\.requiredHeaders/)
  assert.match(source, /upload\.headers/)
  assert.match(source, /resolve\(xhr\)/, 'the caller cannot read the relay response header')
})

// Data space files and folders had the same defect as code archives: the ticket
// named an object-store address the browser could not connect to. They now go
// to the platform as well, and because that endpoint is authenticated the
// ticket has to carry credentials.
test('data space uploads are relayed through the platform with credentials', async () => {
  const source = await read('./api/dataSpaces.js')

  assert.match(source, /createDataSpaceUpload/)
  assert.match(source, /Authorization/, 'the relay ticket carries no credentials')
  assert.match(source, /getToken/)
  // The destination comes from the platform ticket, not from object storage.
  assert.equal(source.includes('requiredHeaders'), false, 'object-store upload headers are still being applied')
})
