/**
 * Copy text without depending on a secure context.
 *
 * `navigator.clipboard` is undefined on plain HTTP and inside some embedded
 * browsers. A command block a user can only read is useless, so fall back to a
 * hidden textarea before reporting failure.
 *
 * @param {string} value
 * @returns {Promise<boolean>} whether the text reached the clipboard
 */
export async function copyToClipboard(value) {
  const text = String(value ?? '')
  if (!text) return false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Fall through to the legacy path below.
  }
  return legacyCopy(text)
}

function legacyCopy(text) {
  if (typeof document === 'undefined') return false
  try {
    const area = document.createElement('textarea')
    area.value = text
    area.setAttribute('readonly', '')
    area.style.position = 'fixed'
    area.style.top = '0'
    area.style.opacity = '0'
    document.body.appendChild(area)
    area.select()
    const copied = document.execCommand('copy')
    document.body.removeChild(area)
    return copied
  } catch {
    return false
  }
}
