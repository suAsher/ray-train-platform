// folderUploadRelativePath accepts the browser's webkitRelativePath exactly as
// a relative object name. The browser is untrusted input: never coerce an
// absolute or traversal path into a valid destination.
export function folderUploadRelativePath(file) {
  const value = String(file?.webkitRelativePath || file?.name || '')
  if (!value || value.startsWith('/') || value.startsWith('\\') || value.includes('\\') || value.includes('\u0000')) {
    throw new Error('目录路径不合法')
  }
  const segments = value.split('/')
  if (segments.some((segment) => !segment || segment === '.' || segment === '..')) {
    throw new Error('目录路径不合法')
  }
  return segments.join('/')
}

// XMLHttpRequest is deliberate here. Fetch cannot expose browser upload
// progress, while presigned PUT uploads must present useful progress and allow
// failed files to be retried independently by the caller.
export function uploadDataSpaceFile(upload, file, options = {}) {
  const onProgress = typeof options.onProgress === 'function' ? options.onProgress : () => {}
  const xhrFactory = options.xhrFactory || (() => new XMLHttpRequest())
  let lastLoaded = -1
  let lastTotal = -1
  const reportProgress = (loaded, total) => {
    const nextLoaded = Math.max(0, Number(loaded || 0))
    const nextTotal = Math.max(0, Number(total || 0))
    if (nextLoaded === lastLoaded && nextTotal === lastTotal) return
    lastLoaded = nextLoaded
    lastTotal = nextTotal
    onProgress({ loaded: nextLoaded, total: nextTotal })
  }

  return new Promise((resolve, reject) => {
    const xhr = xhrFactory()
    xhr.open('PUT', upload.url, true)
    for (const [name, value] of Object.entries(upload.requiredHeaders || {})) {
      xhr.setRequestHeader(name, value)
    }
    xhr.upload.onprogress = (event) => {
      const total = event.lengthComputable ? event.total : Number(file?.size || 0)
      reportProgress(event.loaded, total)
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        reportProgress(file?.size, file?.size)
        resolve()
        return
      }
      reject(uploadError(`上传失败 (${xhr.status || '网络错误'})`, xhr.status || 0, 'DATA_SPACE_UPLOAD_FAILED'))
    }
    xhr.onerror = () => reject(uploadError('上传失败（网络连接中断）', 0, 'DATA_SPACE_UPLOAD_FAILED'))
    xhr.onabort = () => reject(uploadError('上传已取消', 0, 'DATA_SPACE_UPLOAD_ABORTED'))
    xhr.send(file)
  })
}

function uploadError(message, status, code) {
  const error = new Error(message)
  error.status = status
  error.code = code
  return error
}
