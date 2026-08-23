const gitCommitPattern = /^[0-9a-f]{7,64}$/i
const snapshotPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/

export function parseEntrypoint(value) {
  const parts = []
  const matcher = /"([^"\\]*(?:\\.[^"\\]*)*)"|'([^']*)'|([^\s]+)/g
  let match
  while ((match = matcher.exec(String(value || ''))) !== null) {
    parts.push(match[1] ?? match[2] ?? match[3])
  }
  return parts
}

/** Build the copyable spk-rayjob command shown in the final submit preview. */
export function equivalentSubmitCommand(form) {
  const parts = ['spk-rayjob submit', `--name ${form.name || '<任务名>'}`, `--image ${form.image || '<镜像 digest>'}`]
  parts.push(`--entrypoint '${form.entrypoint || '<启动命令>'}'`)
  parts.push(`--workers ${form.workerReplicas}`, `--gpus-per-worker ${form.gpusPerWorker}`)
  if (form.cacheMode === 'runtime') {
    parts.push('--cache-mode runtime', `--cache-size ${String(form.cacheSize || '<缓存容量>').trim()}`)
  }
  if (form.input?.spaceId) {
    parts.push(`--input-space ${form.input.spaceId}`)
    if (form.input.relativePath) parts.push(`--input-path ${form.input.relativePath}`)
  }
  if (form.checkpoint?.spaceId) {
    parts.push(`--checkpoint-space ${form.checkpoint.spaceId}`)
    if (form.checkpoint.relativePath) parts.push(`--checkpoint-path ${form.checkpoint.relativePath}`)
  }
  parts.push('--watch')
  return parts.join(' \\\n  ')
}

export function buildJobSpec(form, platformLimits = {}) {
  const command = parseEntrypoint(form.entrypoint)
  if (command.length === 0) {
    throw new Error('请输入训练启动命令')
  }
  const cache = buildCache(form, platformLimits.cache)

  const spec = {
    name: requiredText(form.name, '任务名称'),
    image: requiredText(form.image, '训练镜像'),
    source: buildCodeSource(form),
    entrypoint: { command: [command[0]], args: command.slice(1) },
    resources: {
      workerReplicas: positiveInteger(form.workerReplicas, '节点数量'),
      gpusPerWorker: positiveInteger(form.gpusPerWorker, '每节点 GPU 数'),
      cpuPerWorker: positiveInteger(form.cpuPerWorker || 8, '每节点 CPU 数'),
      memoryPerWorker: requiredText(form.memoryPerWorker || '32Gi', '每节点内存'),
    },
    execution: { mode: executionMode(form) },
    // Data locations are logical spaces, never a TOS URI, object key, or
    // PVC. The backend derives the caller's permitted root at submission.
    input: dataLocation(form.input, '训练输入'),
    checkpoint: dataLocation(form.checkpoint, 'Checkpoint 输入'),
    output: outputLocation(form.output),
    queue: '',
    timeoutSeconds: nonNegativeInteger(form.timeoutSeconds || 0, '最长运行时间'),
    retryPolicy: { maxRetries: boundedInteger(form.maxRetries || 0, '自动重试次数', 0, 3) },
    ...(cache ? { cache } : {}),
  }

  const priority = String(form.priority || '').trim()
  if (priority) {
    spec.priority = priority
  }
  return spec
}

function buildCache(form, policy) {
  const mode = String(form.cacheMode || 'off').trim() || 'off'
  const size = String(form.cacheSize || '').trim()
  if (mode === 'off') {
    if (size) throw new Error('缓存关闭时不能选择容量')
    return null
  }
  if (mode !== 'runtime') throw new Error('请选择有效的缓存模式')
  if (policy?.enabled !== true || !Array.isArray(policy.modes) || !policy.modes.includes('runtime')) {
    throw new Error('平台未开放运行时缓存')
  }
  if (!size) throw new Error('请选择运行时缓存容量')
  if (!Array.isArray(policy.allowedSizes) || !policy.allowedSizes.includes(size)) {
    throw new Error('运行时缓存容量不在平台允许范围')
  }
  return { mode: 'runtime', size }
}

function executionMode(form) {
  const workers = positiveInteger(form.workerReplicas, '节点数量')
  const gpus = positiveInteger(form.gpusPerWorker, '每节点 GPU 数')
  const mode = String(form.executionMode || (workers === 1 ? (gpus === 1 ? 'single_gpu' : 'torchrun') : 'ray_train')).trim()
  if (mode === 'single_gpu' && (workers !== 1 || gpus !== 1)) {
    throw new Error('单卡模式必须为 1 节点 × 1 GPU')
  }
  if (mode === 'torchrun' && (workers !== 1 || gpus < 2)) {
    throw new Error('单机多卡 DDP 必须为 1 节点且至少申请 2 张 GPU')
  }
  if (mode === 'ray_train' && workers < 2) {
    throw new Error('多机多卡 Ray Train 至少需要 2 个节点')
  }
  if (!['single_gpu', 'torchrun', 'ray_train'].includes(mode)) {
    throw new Error('请选择有效的训练执行方式')
  }
  return mode
}

function buildCodeSource(form) {
  switch (form.codeSourceType) {
    case 'git':
      return {
        type: 'git',
        url: gitURL(form.gitURL),
        commit: gitCommit(form.gitCommit),
      }
    case 'workspace':
      return {
        type: 'workspace',
        snapshot: workspaceSnapshot(form.workspaceSnapshot),
      }
    default:
      throw new Error('代码来源仅支持 Git 仓库或调试工作区代码版本')
  }
}

function requiredText(value, label) {
  const normalized = String(value || '').trim()
  if (!normalized) {
    throw new Error(`${label}不能为空`)
  }
  return normalized
}

function positiveInteger(value, label) {
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < 1) {
    throw new Error(`${label}必须是正整数`)
  }
  return parsed
}

function nonNegativeInteger(value, label) {
  const parsed = Number(value)
  if (!Number.isSafeInteger(parsed) || parsed < 0) {
    throw new Error(`${label}必须是非负整数`)
  }
  return parsed
}

function boundedInteger(value, label, minimum, maximum) {
  const parsed = nonNegativeInteger(value, label)
  if (parsed < minimum || parsed > maximum) {
    throw new Error(`${label}必须在 ${minimum} 到 ${maximum} 之间`)
  }
  return parsed
}

function gitURL(value) {
  const url = new URL(requiredText(value, 'Git 仓库地址'))
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) {
    throw new Error('Git 仓库地址必须是无凭据的 HTTP(S) 地址')
  }
  return url.toString()
}

function gitCommit(value) {
  const commit = requiredText(value, 'Git Commit')
  if (!gitCommitPattern.test(commit)) {
    throw new Error('Git Commit 必须是 7 到 64 位十六进制哈希')
  }
  return commit
}

function workspaceSnapshot(value) {
  const snapshot = requiredText(value, '工作区快照')
  if (!snapshotPattern.test(snapshot)) {
    throw new Error('工作区快照格式不合法')
  }
  return snapshot
}

function dataLocation(value, label) {
  if (value == null || value === '') return {}
  if (typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label}选择不合法`)
  }
  const space = String(value.spaceId || value.space || '').trim()
  const relativePath = String(value.relativePath || '').trim().replace(/\/$/, '')
  if (!space && !relativePath) return {}
  if (!space) {
    throw new Error(`请选择${label}`)
  }
  if (relativePath) {
    if (relativePath.startsWith('/') || relativePath.includes('\\') || relativePath.includes('://') || relativePath.split('/').some((part) => !part || part === '.' || part === '..')) {
      throw new Error(`${label}路径不合法`)
    }
  }
  return relativePath ? { space, relativePath } : { space }
}

function outputLocation(value) {
  const location = dataLocation(value, '训练结果位置')
  if (location.space && location.space !== 'my-runs') {
    throw new Error('训练结果必须保存到“我的训练结果”')
  }
  return location
}
