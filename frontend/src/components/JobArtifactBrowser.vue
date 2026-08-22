<template>
  <section class="space-y-4">
    <div class="flex flex-wrap items-start justify-between gap-3 rounded-xl border border-slate-800 bg-slate-950/50 p-4">
      <div>
        <p class="text-sm font-semibold text-slate-100">训练产物</p>
        <p class="mt-1 text-xs leading-5 text-slate-500">训练脚本写入输出目录的文件会显示在这里。平台不会暴露底层对象存储地址或访问凭据。</p>
      </div>
      <el-button size="small" :loading="loading" @click="loadDirectory()">刷新</el-button>
    </div>

    <el-alert v-if="error" :title="error" type="warning" show-icon :closable="false" class="!rounded-xl" />

    <template v-else>
      <div class="flex flex-wrap items-center gap-1 rounded-xl border border-slate-800 bg-slate-900/60 p-3 text-xs">
        <el-button text size="small" :disabled="!currentPath" @click="goTo('')">本任务输出</el-button>
        <template v-for="(segment, index) in breadcrumbs" :key="`${index}-${segment}`">
          <span class="text-slate-600">/</span>
          <el-button text size="small" :disabled="index === breadcrumbs.length - 1" @click="goTo(breadcrumbs.slice(0, index + 1).join('/'))">{{ segment }}</el-button>
        </template>
      </div>

      <div v-if="loading" class="rounded-xl border border-slate-800 bg-slate-950/40 p-8 text-center text-sm text-slate-500">正在加载训练产物…</div>
      <el-empty v-else-if="entries.length === 0" description="当前任务尚未写入产物。训练脚本可将模型、日志和指标写入输出目录。" />
      <div v-else class="overflow-hidden rounded-xl border border-slate-800">
        <div v-for="entry in entries" :key="`${entry.type}-${entry.name}`" class="flex items-center gap-3 border-b border-slate-800/80 bg-slate-950/30 px-4 py-3 last:border-b-0">
          <el-icon :class="entry.type === 'directory' ? 'text-amber-400' : 'text-blue-400'" :size="18">
            <FolderOpened v-if="entry.type === 'directory'" />
            <Document v-else />
          </el-icon>
          <button v-if="entry.type === 'directory'" type="button" class="min-w-0 flex-1 truncate text-left text-sm font-medium text-slate-100 hover:text-blue-300" @click="enterDirectory(entry.name)">{{ entry.name }}</button>
          <span v-else class="min-w-0 flex-1 truncate text-sm text-slate-200">{{ entry.name }}</span>
          <span class="hidden w-28 text-right text-xs text-slate-500 sm:inline">{{ entry.type === 'file' ? formatBytes(entry.sizeBytes) : '目录' }}</span>
          <span class="hidden w-36 text-right text-xs text-slate-500 md:inline">{{ entry.lastModified ? formatDate(entry.lastModified) : '—' }}</span>
          <div v-if="entry.type === 'file'" class="flex shrink-0 gap-1">
            <el-button text size="small" @click="preview(entry.name)">预览</el-button>
          </div>
        </div>
      </div>

      <div v-if="nextCursor" class="flex justify-center">
        <el-button size="small" :loading="loadingMore" @click="loadDirectory(nextCursor, true)">加载更多</el-button>
      </div>
    </template>

    <el-dialog v-model="previewVisible" :title="previewTitle" width="min(900px, 94vw)" destroy-on-close>
      <div v-if="previewLoading" class="py-12 text-center text-sm text-slate-500">正在加载预览…</div>
      <el-alert v-else-if="previewError" :title="previewError" type="warning" show-icon :closable="false" />
      <pre v-else-if="previewData?.kind === 'text'" class="max-h-[60vh] overflow-auto rounded-lg bg-slate-950 p-4 text-xs leading-6 text-slate-200 whitespace-pre-wrap break-words">{{ previewData.content }}</pre>
      <div v-else-if="previewData?.kind === 'image'" class="flex justify-center rounded-lg bg-slate-950 p-3"><img :src="imagePreviewURL" :alt="previewTitle" class="max-h-[60vh] max-w-full object-contain" /></div>
      <el-empty v-else description="该文件暂不支持在线预览；请在调试环境的个人运行目录中查看。" />
      <template #footer><el-button @click="previewVisible = false">关闭</el-button></template>
    </el-dialog>
  </section>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import { fetchJobArtifactPreview, fetchJobArtifacts } from '../api/jobArtifacts'

const props = defineProps({ jobId: { type: String, required: true } })

const currentPath = ref('')
const entries = ref([])
const nextCursor = ref('')
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const previewVisible = ref(false)
const previewLoading = ref(false)
const previewError = ref('')
const previewTitle = ref('')
const previewData = ref(null)
let requestSequence = 0

const breadcrumbs = computed(() => currentPath.value ? currentPath.value.split('/') : [])
const imagePreviewURL = computed(() => {
  if (previewData.value?.kind !== 'image' || !previewData.value.content) return ''
  return `data:${previewData.value.contentType};base64,${previewData.value.content}`
})

const joinPath = (name) => [currentPath.value, name].filter(Boolean).join('/')

const loadDirectory = async (cursor = '', append = false) => {
  if (!props.jobId) return
  const sequence = ++requestSequence
  if (append) loadingMore.value = true
  else loading.value = true
  error.value = ''
  try {
    const page = await fetchJobArtifacts(props.jobId, currentPath.value, cursor)
    if (sequence !== requestSequence) return
    entries.value = append ? [...entries.value, ...(page.entries || [])] : (page.entries || [])
    nextCursor.value = page.nextCursor || ''
  } catch (requestError) {
    if (sequence !== requestSequence) return
    entries.value = append ? entries.value : []
    nextCursor.value = ''
    error.value = requestError.code === 'ARTIFACTS_NOT_CONFIGURED'
      ? '该任务没有配置可浏览的输出位置。新任务选择输出位置后，产物会自动归档到任务专属目录。'
      : (requestError.message || '无法加载训练产物')
  } finally {
    if (sequence === requestSequence) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

const goTo = (path) => {
  currentPath.value = path
  entries.value = []
  nextCursor.value = ''
  loadDirectory()
}

const enterDirectory = (name) => goTo(joinPath(name))

const preview = async (name) => {
  const artifactPath = joinPath(name)
  previewTitle.value = name
  previewVisible.value = true
  previewLoading.value = true
  previewError.value = ''
  previewData.value = null
  try {
    previewData.value = await fetchJobArtifactPreview(props.jobId, artifactPath)
  } catch (requestError) {
    previewError.value = requestError.code === 'ARTIFACT_PREVIEW_UNSUPPORTED'
      ? '该类型暂不支持在线预览。'
      : (requestError.message || '无法加载文件预览')
  } finally {
    previewLoading.value = false
  }
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

watch(() => props.jobId, () => goTo(''))
onMounted(() => loadDirectory())
</script>
