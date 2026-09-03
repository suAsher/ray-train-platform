// Checkpoints are reachable from two browsers: a task's training artifacts and
// the user's own data space. Both must offer the action for exactly the files
// the API will serve, so the rule lives here rather than in either component.
//
// This mirrors the server-side download policy. A file outside this list is
// rejected with 415, so showing a button for it would only produce an error.
export const CHECKPOINT_EXTENSIONS = ['.pth', '.pt', '.ckpt', '.onnx', '.safetensors']

export function isCheckpointFile(name) {
  const lower = String(name || '').toLowerCase()
  return CHECKPOINT_EXTENSIONS.some((extension) => lower.endsWith(extension))
}

// saveBlobAsFile hands a downloaded body to the browser as a save action.
// The object URL is released on the next tick: revoking it in the same tick can
// abort the download the click just started.
export function saveBlobAsFile(blob, fileName) {
  const objectURL = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = objectURL
  link.download = fileName
  document.body.appendChild(link)
  link.click()
  link.remove()
  setTimeout(() => URL.revokeObjectURL(objectURL), 0)
}
