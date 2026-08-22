function normalizedDirectoryName(value) {
  const name = String(value || '').trim()
  if (!name || name === '.' || name === '..' || name.includes('/') || name.includes('\\') || name.includes('\u0000')) {
    throw new Error('目录名称不合法')
  }
  return name
}

export function appendDataSpaceDirectory(currentPath, directoryName) {
  const name = normalizedDirectoryName(directoryName)
  const current = String(currentPath || '').trim().replace(/^\/+|\/+$/g, '')
  return current ? `${current}/${name}` : name
}

export function parentDataSpaceDirectory(currentPath) {
  const parts = String(currentPath || '').split('/').filter(Boolean)
  parts.pop()
  return parts.join('/')
}

export function selectedDataSpaceDirectoryLabel(spaceName, relativePath) {
  const name = String(spaceName || '').trim() || '已选数据空间'
  const path = String(relativePath || '').trim().replace(/^\/+|\/+$/g, '')
  return path ? `${name} / ${path}` : `${name} /`
}
