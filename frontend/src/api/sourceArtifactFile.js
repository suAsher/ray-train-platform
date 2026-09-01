// Web Crypto hashes a File through one ArrayBuffer. Keep the Portal ceiling
// below the backend/CLI ceiling so a browser cannot allocate multi-GiB memory.
const maxSourceArchiveBytes = 512 * 1024 * 1024

export async function sha256File(file) {
  if (!file || typeof file.arrayBuffer !== 'function') throw new Error('代码包文件不可读')
  if (!globalThis.crypto?.subtle) throw new Error('当前浏览器不支持代码包完整性校验')
  const digest = await globalThis.crypto.subtle.digest('SHA-256', await file.arrayBuffer())
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
}

export function validateSourceArchive(file) {
  const name = String(file?.name || '').toLowerCase()
  const size = Number(file?.size || 0)
  if (!name.endsWith('.zip')) throw new Error('请选择 ZIP 格式的代码包')
  if (!Number.isSafeInteger(size) || size < 1 || size > maxSourceArchiveBytes) {
    throw new Error('Portal 代码包必须大于 0 且不超过 512 MiB')
  }
}
