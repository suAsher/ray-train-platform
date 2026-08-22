import assert from 'node:assert/strict'
import test from 'node:test'

import { folderUploadRelativePath, uploadDataSpaceFile } from './dataSpaceUpload.js'

test('folder upload path preserves nested directories and rejects traversal', () => {
  assert.equal(folderUploadRelativePath({ name: 'train.py', webkitRelativePath: 'demo/src/train.py' }), 'demo/src/train.py')
  assert.throws(() => folderUploadRelativePath({ name: 'train.py', webkitRelativePath: '../private/train.py' }), /目录路径不合法/)
  assert.throws(() => folderUploadRelativePath({ name: 'train.py', webkitRelativePath: '/private/train.py' }), /目录路径不合法/)
})

test('presigned upload reports byte progress and only resolves after an HTTP success', async () => {
  const progress = []
  const xhr = new FakeXMLHttpRequest(200)

  await uploadDataSpaceFile(
    { url: 'https://example.invalid/presigned', requiredHeaders: { 'Content-Type': 'text/plain' } },
    { size: 10, type: 'text/plain' },
    { onProgress: (event) => progress.push(event), xhrFactory: () => xhr },
  )

  assert.deepEqual(progress, [{ loaded: 4, total: 10 }, { loaded: 10, total: 10 }])
  assert.equal(xhr.method, 'PUT')
  assert.equal(xhr.headers['Content-Type'], 'text/plain')
})

test('presigned upload gives the caller a retryable error for a failed HTTP response', async () => {
  const xhr = new FakeXMLHttpRequest(503)

  await assert.rejects(
    uploadDataSpaceFile({ url: 'https://example.invalid/presigned' }, { size: 1 }, { xhrFactory: () => xhr }),
    (error) => error.code === 'DATA_SPACE_UPLOAD_FAILED' && error.status === 503,
  )
})

class FakeXMLHttpRequest {
  constructor(status) {
    this.status = status
    this.headers = {}
    this.upload = {}
  }

  open(method, url) {
    this.method = method
    this.url = url
  }

  setRequestHeader(name, value) {
    this.headers[name] = value
  }

  send() {
    this.upload.onprogress?.({ lengthComputable: true, loaded: 4, total: 10 })
    this.upload.onprogress?.({ lengthComputable: true, loaded: 10, total: 10 })
    this.onload?.()
  }
}
