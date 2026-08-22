const GIB = 1024 * 1024 * 1024
const TIB = 1024 * GIB

// Storage limits are a backend-enforced TOS ObjectSet policy. This helper
// accepts only integer GiB values so the Portal cannot accidentally send a
// fractional or unlimited capacity request.
export function storageQuotaGiBToBytes(gib) {
  if (!Number.isSafeInteger(gib) || gib < 1 || gib > Math.floor(Number.MAX_SAFE_INTEGER / GIB)) return null
  return gib * GIB
}

// Runtime Helm configuration is intentionally passed as a familiar Kubernetes
// quantity (for example 100Gi or 2Ti). The Portal needs the same hard ceiling
// before it submits a request, so administrators receive a local explanation
// instead of a generic ObjectSet 400 response.
export function storageQuotaGiBFromQuantity(value) {
  const match = /^(\d+)(Gi|Ti)$/.exec(String(value || '').trim())
  if (!match) return null
  const amount = Number(match[1])
  if (!Number.isSafeInteger(amount) || amount < 1) return null
  const gib = match[2] === 'Ti' ? amount * 1024 : amount
  return Number.isSafeInteger(gib) ? gib : null
}

export function formatStorageQuota(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '未启用'
  if (bytes % TIB === 0) return `${bytes / TIB} TiB`
  if (bytes % GIB === 0) return `${bytes / GIB} GiB`
  return `${Math.round((bytes / GIB) * 10) / 10} GiB`
}
