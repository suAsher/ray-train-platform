<template>
  <div class="mx-auto max-w-6xl space-y-6">
    <section class="flex flex-wrap items-start justify-between gap-4 rounded-2xl border border-slate-800 bg-[#131826] p-6 shadow-xl">
      <div>
        <p class="text-xs font-semibold uppercase tracking-[0.18em] text-purple-400">My data</p>
        <h1 class="mt-1 text-2xl font-bold text-white">我的数据</h1>
        <p class="mt-2 max-w-3xl text-sm leading-6 text-slate-400">选择训练数据、管理个人文件和查看训练结果。平台自动处理底层对象存储和权限，不会显示桶名、PVC 或密钥。</p>
      </div>
      <div class="flex gap-3">
        <el-button :loading="loading" @click="loadSpaces">刷新</el-button>
        <router-link to="/job/create"><el-button type="primary">使用数据创建训练</el-button></router-link>
      </div>
    </section>

    <el-alert v-if="error" type="warning" :closable="false"><template #title>{{ error }}</template></el-alert>

    <section class="rounded-2xl border border-emerald-900/60 bg-emerald-950/15 p-6 shadow-xl">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="text-lg font-semibold text-white">个人空间</h2>
            <el-tag size="small" type="success" effect="plain">读写</el-tag>
          </div>
          <p class="mt-2 text-sm leading-6 text-slate-400">调试和训练环境中的个人持久化存储根目录。任务结束或调试环境关闭后，文件仍会保留。</p>
        </div>
        <code class="rounded-lg border border-emerald-900/70 bg-slate-950/60 px-3 py-2 font-mono text-sm text-emerald-300">/mnt/storage/me</code>
      </div>

      <div class="mt-5 grid gap-3 md:grid-cols-2">
        <button type="button" class="group rounded-xl border border-slate-800 bg-slate-950/35 p-4 text-left transition hover:border-blue-500/70 hover:bg-blue-950/20" @click="selectSpaceByID('my-files')">
          <span class="flex items-center justify-between gap-3">
            <span class="font-medium text-slate-100 group-hover:text-blue-200">我的文件</span>
            <span class="text-xs text-blue-300">进入目录 →</span>
          </span>
          <span class="mt-2 block font-mono text-xs text-slate-400">/mnt/storage/me/files</span>
          <span class="mt-2 block text-xs leading-5 text-slate-500">上传和管理个人数据、代码附件及其他持久文件。</span>
        </button>
        <button type="button" class="group rounded-xl border border-slate-800 bg-slate-950/35 p-4 text-left transition hover:border-purple-500/70 hover:bg-purple-950/20" @click="selectSpaceByID('my-runs')">
          <span class="flex items-center justify-between gap-3">
            <span class="font-medium text-slate-100 group-hover:text-purple-200">我的运行结果</span>
            <span class="text-xs text-purple-300">进入目录 →</span>
          </span>
          <span class="mt-2 block font-mono text-xs text-slate-400">/mnt/storage/me/runs</span>
          <span class="mt-2 block text-xs leading-5 text-slate-500">查看训练输出、Checkpoint、模型权重和任务产物。</span>
        </button>
      </div>
    </section>

    <section class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <button
        v-for="space in spaces"
        :key="space.id"
        type="button"
        class="rounded-2xl border p-5 text-left transition"
        :class="selectedSpace?.id === space.id ? 'border-blue-400 bg-blue-950/25 shadow-lg shadow-blue-950/30' : 'border-slate-800 bg-[#131826] hover:border-slate-600'"
        @click="selectSpace(space)"
      >
        <div class="flex items-start justify-between gap-3">
          <div>
            <p class="font-semibold text-white">{{ space.name }}</p>
            <p class="mt-1 text-xs leading-5 text-slate-500">{{ space.description }}</p>
          </div>
          <el-tag size="small" effect="plain" :type="accessType(space)">{{ accessLabel(space) }}</el-tag>
        </div>
        <div class="mt-4 flex items-center justify-between gap-3 text-xs">
          <span class="font-mono text-blue-300">{{ space.mountPath }}</span>
          <span class="flex items-center gap-2">
            <el-tag v-if="space.provider === 'tos'" size="small" :type="storageStatusType(space.storageStatus)">{{ storageStatusLabel(space.storageStatus) }}</el-tag>
            <el-tag size="small" :type="statusType(space.mountStatus)">{{ statusLabel(space) }}</el-tag>
          </span>
        </div>
        <p class="mt-3 text-xs text-slate-500">{{ space.browseEnabled ? storageReady(space) ? dataSpaceReadiness(space).ready ? '个人空间与 GPU 挂载均已就绪，点击浏览文件和目录。' : '个人空间已就绪，可浏览和上传；GPU 挂载配置完成后才可用于调试和训练。' : dataSpaceReadiness(space).message : '管理员登记后会在调试与训练环境中只读可见' }}</p>
      </button>
    </section>

    <section v-if="selectedSpace" class="rounded-2xl border border-slate-800 bg-[#131826] p-6 shadow-xl">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p class="text-xs font-semibold uppercase tracking-[0.16em] text-slate-500">Data explorer</p>
          <h2 class="mt-1 text-lg font-semibold text-white">{{ selectedSpace.name }}</h2>
          <p class="mt-1 text-sm text-slate-400">{{ dataSpaceDescription(selectedSpace) }}</p>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <el-tag effect="plain" :type="accessType(selectedSpace)">{{ accessLabel(selectedSpace) }}</el-tag>
          <el-tag v-if="selectedSpace.provider === 'tos'" :type="storageStatusType(selectedSpace.storageStatus)">{{ storageStatusLabel(selectedSpace.storageStatus) }}</el-tag>
          <el-tag :type="statusType(selectedSpace.mountStatus)">{{ statusLabel(selectedSpace) }}</el-tag>
          <router-link :to="{ path: '/job/create', query: pickerQuery }"><el-button type="primary" :disabled="!canUseForTraining(selectedSpace)">使用当前目录训练</el-button></router-link>
        </div>
      </div>

      <div v-if="!selectedSpace.browseEnabled" class="mt-6 rounded-xl border border-dashed border-slate-700 bg-slate-950/40 p-5 text-sm leading-6 text-slate-400">
        该 IDC 数据空间不通过网页枚举目录；管理员配置完成后，在 GPU 调试环境中会以 <code>{{ selectedSpace.mountPath }}</code> 只读可见。
      </div>
      <template v-else>
        <el-alert v-if="!storageReady(selectedSpace)" class="mt-6" type="warning" :closable="false">
          <template #title>{{ dataSpaceReadiness(selectedSpace).message }}</template>
        </el-alert>
        <el-alert v-else-if="!dataSpaceReadiness(selectedSpace).ready" class="mt-6" type="info" :closable="false">
          <template #title>个人对象空间已就绪：你可以在这里管理已授权的文件。GPU 挂载仍在等待平台存储验收；完成后才可用于调试和训练。</template>
        </el-alert>
        <div class="mt-6 flex flex-wrap items-center gap-3 rounded-xl border border-slate-800 bg-slate-950/50 p-3">
          <el-button size="small" :disabled="!currentPath" @click="goUp">返回上级</el-button>
          <span class="min-w-0 flex-1 truncate font-mono text-xs text-blue-300">{{ displayPath }}</span>
          <el-button v-if="canMutate" size="small" @click="folderDialogVisible = true">{{ selectedSpace.id === 'team-shared' ? '新建发布目录' : '新建文件夹' }}</el-button>
          <el-button v-if="canMutate" size="small" type="primary" :loading="uploading" @click="fileInput?.click()">{{ selectedSpace.id === 'team-shared' ? '发布文件' : '上传文件' }}</el-button>
          <el-button v-if="selectedSpace.id === 'workspace' && canMutate" size="small" :loading="uploading" @click="folderInput?.click()">上传代码文件夹</el-button>
          <el-button v-if="selectedSpace.id === 'workspace' && canMutate" size="small" type="warning" :loading="creatingSnapshot" @click="createSnapshot">创建训练代码版本</el-button>
          <input ref="fileInput" type="file" class="hidden" @change="uploadFile">
          <input ref="folderInput" type="file" webkitdirectory multiple class="hidden" @change="uploadFolder">
          <el-button size="small" :loading="loadingEntries" @click="loadEntries">刷新目录</el-button>
        </div>

        <div v-if="uploadState" class="mt-4 rounded-xl border border-blue-900/60 bg-blue-950/20 p-4">
          <div class="flex flex-wrap items-center justify-between gap-2 text-xs text-slate-300">
            <span>{{ uploadState.retrying ? '正在重试失败文件' : '正在上传' }}：{{ uploadState.currentFile }}</span>
            <span>{{ uploadState.completedFiles }}/{{ uploadState.totalFiles }} 个文件 · {{ formatBytes(uploadState.uploadedBytes) }} / {{ formatBytes(uploadState.totalBytes) }}</span>
          </div>
          <el-progress class="mt-3" :percentage="uploadPercentage" :stroke-width="8" :show-text="false" />
        </div>
        <el-alert v-if="failedUploads.length" class="mt-4" type="warning" :closable="false">
          <template #title>{{ failedUploads.length }} 个文件未上传。已成功的文件不会重复上传；请修复网络或权限问题后重试失败文件。</template>
          <div class="mt-2 flex flex-wrap items-center gap-2 text-xs text-slate-600">
            <span class="max-w-full truncate">{{ failedUploads[0].path }}：{{ failedUploads[0].message }}</span>
            <el-button v-if="canMutate" size="small" type="warning" :loading="uploading" @click="retryFailedUploads">重试失败文件</el-button>
          </div>
        </el-alert>

        <div v-if="loadingEntries" class="mt-5 text-sm text-slate-500">正在读取目录…</div>
        <div v-else-if="entries.length" class="mt-5 overflow-hidden rounded-xl border border-slate-800">
          <div v-for="entry in entries" :key="`${entry.type}-${entry.name}`" class="flex items-center gap-3 border-b border-slate-800/80 bg-slate-950/30 px-4 py-3 last:border-b-0">
            <span class="text-lg">{{ entry.type === 'directory' ? '📁' : '📄' }}</span>
            <button v-if="entry.type === 'directory'" type="button" class="min-w-0 flex-1 truncate text-left text-sm font-medium text-slate-100 hover:text-blue-300" @click="openDirectory(entry.name)">{{ entry.name }}</button>
            <span v-else class="min-w-0 flex-1 truncate text-sm text-slate-200">{{ entry.name }}</span>
            <span class="hidden w-24 text-right text-xs text-slate-500 sm:inline">{{ entry.type === 'file' ? formatBytes(entry.sizeBytes) : '目录' }}</span>
            <span class="hidden w-36 text-right text-xs text-slate-500 md:inline">{{ entry.lastModified ? formatDate(entry.lastModified) : '—' }}</span>
          </div>
        </div>
        <div v-else class="mt-5 rounded-xl border border-dashed border-slate-700 bg-slate-950/40 p-5 text-sm leading-6 text-slate-500">
          <template v-if="selectedSpace.id === 'my-files'">
            当前目录为空。我的文件只显示 /mnt/storage/me/files 中的个人数据；训练权重和任务输出请到“我的运行结果”查看。
          </template>
          <template v-else>这个目录暂无可见文件或子目录。</template>
        </div>
        <div v-if="selectedSpace.id === 'workspace'" class="mt-5 rounded-xl border border-blue-900/60 bg-blue-950/20 p-4 text-xs leading-5 text-slate-300">
          当前目录可创建不可变代码版本。创建后，在“新建训练任务 → 调试快照”中选择即可；之后继续修改工作区不会影响已提交的训练。
        </div>
        <div v-if="selectedSpace.id === 'team-shared'" class="mt-5 rounded-xl border border-amber-900/60 bg-amber-950/20 p-4 text-xs leading-5 text-amber-100/80">
          团队共享数据在调试和训练 Pod 内始终为只读。只有团队管理员能在此页面发布数据；请按版本目录发布（例如 <code>datasets/project-a/v1</code>），发布后普通成员即可选择为训练输入，不能在 Pod 内改写。
        </div>
      </template>
    </section>

    <section class="grid gap-4 rounded-2xl border border-blue-900/60 bg-blue-950/20 p-6 md:grid-cols-3">
      <div><p class="font-semibold text-blue-100">1. 准备代码与数据</p><p class="mt-2 text-xs leading-5 text-slate-400">在“我的工作区”上传或同步代码；将个人数据上传到“我的文件”，团队、公共和 IDC 数据可选作只读输入。</p></div>
      <div><p class="font-semibold text-blue-100">2. 在 GPU 调试环境验证</p><p class="mt-2 text-xs leading-5 text-slate-400">调试环境中个人工作区可写，团队、公共和 IDC 数据只读；可在工作区创建持久化 <code>.venv</code>，无需配置对象存储凭据。</p></div>
      <div><p class="font-semibold text-blue-100">3. 固化代码并提交训练</p><p class="mt-2 text-xs leading-5 text-slate-400">在“我的工作区”创建不可变代码版本；新建任务选择该版本、镜像与 1/8/16 卡资源。脚本通过 <code>PLATFORM_DATASET_PATH</code> 读取数据，通过 <code>PLATFORM_OUTPUT_PATH</code> 写入结果。</p></div>
    </section>

    <el-dialog v-model="folderDialogVisible" title="新建文件夹" width="min(420px, 92vw)">
      <el-input v-model="newFolderName" placeholder="例如：datasets/version-1" @keyup.enter="createFolder" />
      <template #footer><el-button @click="folderDialogVisible = false">取消</el-button><el-button type="primary" :loading="creatingFolder" :disabled="!canMutate" @click="createFolder">创建</el-button></template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { createDataSpaceFolder, createDataSpaceUpload, createWorkspaceSnapshot, fetchDataSpaceEntries, fetchDataSpaces, uploadDataSpaceFile } from '../../api/dataSpaces'
import { folderUploadRelativePath } from '../../api/dataSpaceUpload'
import { canManageDataSpace, canMutateDataSpace, canUseDataSpaceForTraining, dataSpaceAccessLabel, dataSpaceAccessType } from '../../dataSpaceActions'
import { dataSpaceReadiness, dataSpaceStorageReady } from '../../dataSpaceReadiness'
import { session } from '../../stores/session'

const maxUploadBytes = 5 * 1024 * 1024 * 1024
const loading = ref(false)
const loadingEntries = ref(false)
const uploading = ref(false)
const creatingFolder = ref(false)
const creatingSnapshot = ref(false)
const error = ref('')
const spaces = ref([])
const selectedSpace = ref(null)
const currentPath = ref('')
const entries = ref([])
const fileInput = ref(null)
const folderInput = ref(null)
const folderDialogVisible = ref(false)
const newFolderName = ref('')
const uploadState = ref(null)
const failedUploads = ref([])
let refreshTimer

const displayPath = computed(() => currentPath.value ? `./${currentPath.value}` : './')
const pickerQuery = computed(() => ({ dataSpace: selectedSpace.value?.id || '', dataPath: currentPath.value || '' }))
const canMutate = computed(() => canMutateDataSpace(selectedSpace.value, session.value))
const uploadPercentage = computed(() => {
  if (!uploadState.value?.totalBytes) return 0
  return Math.min(100, Math.floor((uploadState.value.uploadedBytes / uploadState.value.totalBytes) * 100))
})

const statusLabel = (space) => ({ ready: 'GPU 挂载已就绪', pending: 'GPU 挂载准备中', failed: 'GPU 挂载失败', 'not-configured': space.provider === 'idc' ? '等待管理员登记' : 'GPU 挂载待配置' }[space.mountStatus] || '状态待确认')
const statusType = (status) => ({ ready: 'success', pending: 'warning', failed: 'danger', 'not-configured': 'info' }[status] || 'info')
const storageStatusLabel = (status) => ({ ready: '个人空间已就绪', 'not-configured': '个人空间待配置' }[status] || '状态待确认')
const storageStatusType = (status) => ({ ready: 'success', 'not-configured': 'warning' }[status] || 'info')
const storageReady = dataSpaceStorageReady
const accessLabel = (space) => dataSpaceAccessLabel(space, session.value)
const accessType = (space) => dataSpaceAccessType(space, session.value)
const canUseForTraining = canUseDataSpaceForTraining
const joinPath = (name) => [currentPath.value, name].filter(Boolean).join('/')
const dataSpaceDescription = (space) => {
  if (space?.id === 'team-shared') return canManageDataSpace(space, session.value)
    ? '管理员可在此发布版本化共享数据；调试与训练环境始终以只读方式使用。'
    : '团队已发布的数据，只读；可直接选择为训练输入。'
  if (space?.readOnly) return '此空间仅可读取，可直接选为训练输入。'
  return '此空间可读写：可管理文件、用作调试工作区或保存训练结果。'
}

const scheduleRefresh = () => {
  window.clearTimeout(refreshTimer)
  if (spaces.value.some((space) => space.mountStatus === 'pending')) refreshTimer = window.setTimeout(loadSpaces, 5000)
}

const loadSpaces = async () => {
  loading.value = true
  error.value = ''
  try {
    spaces.value = await fetchDataSpaces()
    const refreshed = spaces.value.find((space) => space.id === selectedSpace.value?.id)
    if (refreshed) selectedSpace.value = refreshed
    else if (!selectedSpace.value && spaces.value.length) await selectSpace(spaces.value[0])
  } catch (requestError) {
    error.value = requestError.message || '无法加载我的数据，请稍后重试。'
  } finally {
    loading.value = false
    scheduleRefresh()
  }
}

const selectSpace = async (space) => {
  selectedSpace.value = space
  currentPath.value = ''
  entries.value = []
  if (space.browseEnabled) await loadEntries()
}

const selectSpaceByID = async (id) => {
  const space = spaces.value.find((candidate) => candidate.id === id)
  if (space) await selectSpace(space)
}

const loadEntries = async () => {
  if (!selectedSpace.value?.browseEnabled) return
  loadingEntries.value = true
  error.value = ''
  try {
    const page = await fetchDataSpaceEntries(selectedSpace.value.id, currentPath.value)
    entries.value = page.entries || []
  } catch (requestError) {
    entries.value = []
    error.value = requestError.message || '无法读取目录，请稍后重试。'
  } finally {
    loadingEntries.value = false
  }
}

const openDirectory = async (name) => {
  currentPath.value = joinPath(name)
  await loadEntries()
}

const goUp = async () => {
  const segments = currentPath.value.split('/').filter(Boolean)
  segments.pop()
  currentPath.value = segments.join('/')
  await loadEntries()
}

const createFolder = async () => {
  if (!canMutate.value) return
  const path = joinPath(String(newFolderName.value || '').trim())
  if (!path) return
  creatingFolder.value = true
  try {
    await createDataSpaceFolder(selectedSpace.value.id, path)
    newFolderName.value = ''
    folderDialogVisible.value = false
    await loadEntries()
    ElMessage.success('文件夹已创建')
  } catch (requestError) {
    ElMessage.error(requestError.message || '创建文件夹失败')
  } finally {
    creatingFolder.value = false
  }
}

const createSnapshot = async () => {
  if (selectedSpace.value?.id !== 'workspace' || !canMutate.value) return
  creatingSnapshot.value = true
  try {
    const snapshot = await createWorkspaceSnapshot(currentPath.value)
    ElMessage.success('训练代码版本已创建，可在新建训练任务中选择')
    window.setTimeout(() => window.location.assign(`/job/create?from=workspace_snapshot&snapshot=${encodeURIComponent(snapshot.id)}`), 350)
  } catch (requestError) {
    ElMessage.error(requestError.message || '创建训练代码版本失败')
  } finally {
    creatingSnapshot.value = false
  }
}

const uploadFile = async (event) => {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file) return
  if (file.size < 1 || file.size > maxUploadBytes) {
    ElMessage.warning('单个文件必须大于 0 且不超过 5 GiB')
    return
  }
  await uploadItems([{ file, path: file.name }], '文件已上传')
}

const uploadFolder = async (event) => {
  const files = Array.from(event.target.files || [])
  event.target.value = ''
  if (!files.length) return
  if (files.some((file) => file.size < 1 || file.size > maxUploadBytes)) {
    ElMessage.warning('代码文件夹中的每个文件必须大于 0 且不超过 5 GiB')
    return
  }
  try {
    const items = files.map((file) => ({ file, path: folderUploadRelativePath(file) }))
    await uploadItems(items, `已上传 ${files.length} 个代码文件`)
  } catch (requestError) {
    ElMessage.error(requestError.message || '上传代码文件夹失败')
  }
}

const uploadItems = async (items, successMessage, retrying = false) => {
  if (!items.length || !selectedSpace.value || !canMutate.value) return
  const totalBytes = items.reduce((sum, item) => sum + Number(item.file.size || 0), 0)
  const nextState = {
    totalFiles: items.length, completedFiles: 0, totalBytes, uploadedBytes: 0,
    currentFile: items[0].path, retrying,
  }
  const failures = []
  let successfulBytes = 0
  uploading.value = true
  uploadState.value = nextState
  try {
    for (const item of items) {
      nextState.currentFile = item.path
      const completedBytes = successfulBytes
      try {
        const upload = await createDataSpaceUpload(selectedSpace.value.id, joinPath(item.path), item.file.type || 'application/octet-stream', item.file.size)
        await uploadDataSpaceFile(upload, item.file, {
          onProgress: ({ loaded }) => {
            nextState.uploadedBytes = Math.min(nextState.totalBytes, completedBytes + loaded)
          },
        })
        nextState.completedFiles += 1
        successfulBytes += Number(item.file.size || 0)
        nextState.uploadedBytes = Math.min(nextState.totalBytes, successfulBytes)
      } catch (requestError) {
        nextState.uploadedBytes = Math.min(nextState.totalBytes, successfulBytes)
        failures.push({ ...item, message: requestError.message || '上传失败' })
      }
    }
    failedUploads.value = failures
    if (nextState.completedFiles) await loadEntries()
    if (failures.length) {
      ElMessage.warning(`已上传 ${nextState.completedFiles}/${items.length} 个文件，${failures.length} 个文件可单独重试`)
    } else {
      ElMessage.success(successMessage)
    }
  } finally {
    uploading.value = false
    uploadState.value = null
  }
}

const retryFailedUploads = async () => {
  if (!canMutate.value) return
  const items = failedUploads.value.map(({ file, path }) => ({ file, path }))
  failedUploads.value = []
  await uploadItems(items, `已重试并上传 ${items.length} 个文件`, true)
}

const formatBytes = (value) => {
  const bytes = Number(value || 0)
  if (!Number.isFinite(bytes) || bytes < 1024) return `${Math.max(0, bytes)} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  const index = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)) - 1)
  return `${(bytes / (1024 ** (index + 1))).toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}
const formatDate = (value) => {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', { hour12: false })
}

onMounted(loadSpaces)
onUnmounted(() => window.clearTimeout(refreshTimer))
</script>

<style scoped>
code { color: rgb(147 197 253); }
</style>
