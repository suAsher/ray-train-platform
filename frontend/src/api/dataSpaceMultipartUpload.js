import { sha256 } from '@noble/hashes/sha256'
import { bytesToHex } from '@noble/hashes/utils'

const HASH_CHUNK_BYTES = 8 * 1024 * 1024
const DEFAULT_CONCURRENCY = 3
const MAX_ATTEMPTS = 4

export function multipartPartBounds(ticket, partNumber) {
  const partSize = Number(ticket.partSizeBytes)
  const totalParts = Number(ticket.totalParts)
  const size = Number(ticket.sizeBytes)
  if (!Number.isSafeInteger(partSize) || partSize < 1 || !Number.isInteger(partNumber) || partNumber < 1 || partNumber > totalParts) {
    throw new Error('分片计划不合法')
  }
  const start = (partNumber - 1) * partSize
  return { start, end: Math.min(size, start + partSize), size: Math.min(size, start + partSize) - start }
}

export async function hashBlobSHA256(blob) {
  const hasher = sha256.create()
  for (let offset = 0; offset < blob.size; offset += HASH_CHUNK_BYTES) {
    const bytes = new Uint8Array(await blob.slice(offset, Math.min(blob.size, offset + HASH_CHUNK_BYTES)).arrayBuffer())
    hasher.update(bytes)
  }
  return bytesToHex(hasher.digest())
}

function partURL(ticket, partNumber) {
  return `/api/v1/data-spaces/${encodeURIComponent(ticket.spaceId)}/uploads/${encodeURIComponent(ticket.sessionId)}/parts/${partNumber}`
}

export function uploadMultipartPart(ticket, partNumber, blob, digest, options = {}) {
  const xhrFactory = options.xhrFactory || (() => new XMLHttpRequest())
  const onProgress = typeof options.onProgress === 'function' ? options.onProgress : () => {}
  return new Promise((resolve, reject) => {
    const xhr = xhrFactory()
    xhr.open('PUT', partURL(ticket, partNumber), true)
    for (const [name, value] of Object.entries(ticket.headers || {})) xhr.setRequestHeader(name, value)
    xhr.setRequestHeader('X-Part-SHA256', digest)
    xhr.upload.onprogress = (event) => onProgress(event.loaded)
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve(xhr)
      else reject(multipartError(`分片 ${partNumber} 上传失败 (${xhr.status || '网络错误'})`, xhr.status || 0))
    }
    xhr.onerror = () => reject(multipartError(`分片 ${partNumber} 上传时网络连接中断`, 0))
    xhr.onabort = () => reject(multipartError('上传已取消', 0, 'DATA_SPACE_UPLOAD_ABORTED'))
    xhr.send(blob)
  })
}

export async function completeMultipartUpload(ticket) {
	const { apiPost } = await import('./client.js')
  return apiPost(`/api/v1/data-spaces/${encodeURIComponent(ticket.spaceId)}/uploads/${encodeURIComponent(ticket.sessionId)}/complete`, {})
}

export async function abortMultipartUpload(ticket) {
	const { apiDelete } = await import('./client.js')
  return apiDelete(`/api/v1/data-spaces/${encodeURIComponent(ticket.spaceId)}/uploads/${encodeURIComponent(ticket.sessionId)}`)
}

export async function uploadMultipartDataSpaceFile(ticket, file, options = {}) {
  if (ticket.mode !== 'multipart' || !ticket.sessionId || !ticket.spaceId || Number(file?.size) !== Number(ticket.sizeBytes)) {
    throw new Error('文件与可恢复上传会话不匹配')
  }
  const concurrency = Math.max(1, Math.min(3, Number(options.concurrency || DEFAULT_CONCURRENCY)))
  const hashPart = options.hashBlob || hashBlobSHA256
  const sendPart = options.uploadPart || uploadMultipartPart
  const finish = options.completeUpload || completeMultipartUpload
  const sleep = options.sleep || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)))
  const onProgress = typeof options.onProgress === 'function' ? options.onProgress : () => {}
  const onPart = typeof options.onPart === 'function' ? options.onPart : () => {}
  const completed = new Map((ticket.completedParts || []).map((part) => [Number(part.partNumber), part]))
  const partProgress = new Map()
  const report = () => {
    let loaded = 0
    for (let number = 1; number <= Number(ticket.totalParts); number += 1) loaded += Number(partProgress.get(number) || 0)
    onProgress({ loaded: Math.min(Number(ticket.sizeBytes), loaded), total: Number(ticket.sizeBytes) })
  }
  const queue = Array.from({ length: Number(ticket.totalParts) }, (_, index) => index + 1)
  persistResume(ticket, file)

  async function processPart(partNumber) {
    const bounds = multipartPartBounds(ticket, partNumber)
    const blob = file.slice(bounds.start, bounds.end)
    onPart({ partNumber, totalParts: Number(ticket.totalParts), state: 'hashing' })
    const digest = await hashPart(blob)
    const receipt = completed.get(partNumber)
    if (receipt && Number(receipt.sizeBytes) === bounds.size && String(receipt.sha256) === digest) {
      partProgress.set(partNumber, bounds.size)
      onPart({ partNumber, totalParts: Number(ticket.totalParts), state: 'resumed' })
      report()
      return
    }
    let lastError
    for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt += 1) {
      partProgress.set(partNumber, 0)
      onPart({ partNumber, totalParts: Number(ticket.totalParts), state: attempt === 1 ? 'uploading' : 'retrying', attempt })
      try {
        await sendPart(ticket, partNumber, blob, digest, {
          ...options,
          onProgress: (loaded) => {
            partProgress.set(partNumber, Math.min(bounds.size, Number(loaded || 0)))
            report()
          },
        })
        partProgress.set(partNumber, bounds.size)
        onPart({ partNumber, totalParts: Number(ticket.totalParts), state: 'completed', attempt })
        report()
        return
      } catch (error) {
        lastError = error
        if (!isRetryable(error) || attempt === MAX_ATTEMPTS) break
        await sleep(500 * (2 ** (attempt - 1)))
      }
    }
    throw lastError
  }

  let cursor = 0
  async function worker() {
    while (cursor < queue.length) {
      const index = cursor
      cursor += 1
      await processPart(queue[index])
    }
  }
  await Promise.all(Array.from({ length: Math.min(concurrency, queue.length) }, () => worker()))
  const result = await finish(ticket)
  clearResume(ticket)
  onProgress({ loaded: Number(ticket.sizeBytes), total: Number(ticket.sizeBytes) })
  return result
}

function isRetryable(error) {
  const status = Number(error?.status || 0)
  return status === 0 || status === 408 || status === 429 || status >= 500
}

function multipartError(message, status, code = 'DATA_SPACE_UPLOAD_PART_FAILED') {
  const error = new Error(message)
  error.status = status
  error.code = code
  return error
}

function resumeKey(ticket) {
  return `ray-platform:data-upload:${ticket.spaceId}:${ticket.sessionId}`
}

function persistResume(ticket, file) {
  try {
    globalThis.localStorage?.setItem(resumeKey(ticket), JSON.stringify({ sessionId: ticket.sessionId, spaceId: ticket.spaceId, name: file.name, size: file.size, lastModified: file.lastModified, expiresAt: ticket.expiresAt }))
  } catch {
    // Resume metadata is an optimization; the durable source of truth is the backend.
  }
}

function clearResume(ticket) {
  try { globalThis.localStorage?.removeItem(resumeKey(ticket)) } catch { /* best effort */ }
}
