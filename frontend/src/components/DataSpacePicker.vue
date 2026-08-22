<template>
  <div class="rounded-2xl border border-slate-800 bg-slate-950/40 p-5">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <p class="text-sm font-semibold text-slate-100">{{ label }}</p>
        <p class="mt-1 text-xs leading-5 text-slate-500">{{ help }}</p>
      </div>
      <el-tag v-if="output" size="small" type="success" effect="plain">平台管理结果目录</el-tag>
    </div>

    <div v-if="loading" class="mt-4 text-xs text-slate-500">正在加载可用数据空间…</div>
    <el-alert v-else-if="loadError" class="mt-4" type="warning" :closable="false">
      <template #title>{{ loadError }}</template>
    </el-alert>
    <div v-else class="mt-4 grid gap-3 md:grid-cols-2">
      <button
        v-for="space in selectableSpaces"
        :key="space.id"
        type="button"
        :disabled="!isReady(space)"
        class="rounded-xl border p-4 text-left transition"
        :class="isSelected(space) ? 'border-blue-400 bg-blue-950/30' : isReady(space) ? 'border-slate-700 bg-slate-900/40 hover:border-slate-500' : 'cursor-not-allowed border-slate-800 bg-slate-950/30 opacity-60'"
        @click="selectSpace(space)"
      >
        <div class="flex items-start justify-between gap-2">
          <span class="font-medium text-slate-100">{{ space.name }}</span>
          <el-tag size="small" :type="accessType(space)">{{ accessLabel(space) }}</el-tag>
        </div>
        <p class="mt-2 text-xs text-slate-500">{{ space.description }}</p>
        <p class="mt-3 font-mono text-[11px] text-blue-300">{{ space.mountPath }}</p>
        <p v-if="!isReady(space)" class="mt-2 text-[11px] text-amber-400">{{ readinessMessage(space) }}</p>
      </button>
    </div>

    <div v-if="model.spaceId && selectedSpace" class="mt-4 rounded-xl border border-slate-800 bg-slate-950/40 p-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <p class="text-xs text-slate-500">{{ output ? '结果将保存到' : '已选择目录' }}</p>
          <p class="mt-1 font-mono text-xs text-blue-300">{{ selectionLabel }}</p>
        </div>
        <el-button v-if="selectedSpace.browseEnabled" size="small" @click="openBrowser">浏览并选择目录</el-button>
      </div>
      <!-- Users previously had to infer this mapping themselves, which is the
           single most common reason a first training run fails. -->
      <div v-if="containerPath" class="mt-3 rounded-lg border border-slate-800 bg-slate-900/50 px-3 py-2">
        <p class="text-[11px] text-slate-500">训练容器内看到的路径</p>
        <p class="mt-1 font-mono text-xs text-emerald-300">{{ containerPath }}</p>
        <p v-if="envVariable" class="mt-1 text-[11px] text-slate-500">
          脚本中请读取环境变量 <code class="text-blue-300">{{ envVariable }}</code>，不要写死绝对路径。
        </p>
      </div>
      <p v-if="output" class="mt-3 text-[11px] leading-5 text-slate-500">平台会在这个目录下自动创建一个与任务绑定的独立子目录，不会覆盖已有结果。</p>
      <p v-else-if="!selectedSpace.browseEnabled" class="mt-3 text-[11px] text-slate-500">IDC 数据以登记的只读根目录挂入训练容器，不提供网页目录枚举。</p>
      <p v-else class="mt-3 text-[11px] text-slate-500">通过目录浏览器选择；无需填写 TOS、桶名、PVC 或路径。</p>
    </div>
    <el-alert v-if="model.spaceId && !isReady(selectedSpace)" class="mt-4" type="warning" :closable="false">
      <template #title>{{ readinessMessage(selectedSpace) }}</template>
    </el-alert>

    <el-dialog v-model="browserVisible" :title="`${selectedSpace?.name || '数据'}：选择目录`" width="min(720px, 94vw)" destroy-on-close>
      <template v-if="selectedSpace?.browseEnabled">
        <div class="flex flex-wrap items-center gap-3 rounded-xl border border-slate-800 bg-slate-950/60 p-3">
          <el-button size="small" :disabled="!browserPath" @click="goUp">返回上级</el-button>
          <span class="min-w-0 flex-1 truncate font-mono text-xs text-blue-300">{{ browserLabel }}</span>
          <el-button size="small" :loading="loadingEntries" @click="loadEntries">刷新</el-button>
        </div>
        <div v-if="loadingEntries" class="py-10 text-center text-sm text-slate-500">正在读取目录…</div>
        <el-alert v-else-if="browserError" class="mt-4" type="warning" :closable="false"><template #title>{{ browserError }}</template></el-alert>
        <div v-else-if="entries.length" class="mt-4 overflow-hidden rounded-xl border border-slate-800">
          <button
            v-for="entry in entries"
            :key="entry.name"
            type="button"
            class="flex w-full items-center gap-3 border-b border-slate-800 px-4 py-3 text-left last:border-b-0 hover:bg-slate-800/50"
            :disabled="entry.type !== 'directory'"
            :class="entry.type === 'directory' ? 'text-slate-100' : 'cursor-not-allowed text-slate-600'"
            @click="entry.type === 'directory' && enterDirectory(entry.name)"
          >
            <span>{{ entry.type === 'directory' ? '📁' : '📄' }}</span>
            <span class="min-w-0 flex-1 truncate text-sm">{{ entry.name }}</span>
            <span v-if="entry.type === 'directory'" class="text-xs text-blue-300">进入</span>
          </button>
        </div>
        <div v-else class="mt-4 rounded-xl border border-dashed border-slate-700 p-6 text-center text-sm text-slate-500">当前目录没有可进入的子目录。</div>
      </template>
      <template #footer>
        <el-button @click="browserVisible = false">取消</el-button>
        <el-button type="primary" :disabled="!selectedSpace || !isReady(selectedSpace)" @click="confirmDirectory">选择当前目录</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { fetchDataSpaceEntries, fetchDataSpaces } from '../api/dataSpaces'
import { dataSpaceAccessLabel, dataSpaceAccessType } from '../dataSpaceActions'
import { dataSpaceReadiness } from '../dataSpaceReadiness'
import { appendDataSpaceDirectory, parentDataSpaceDirectory, selectedDataSpaceDirectoryLabel } from '../dataSpaceSelection'
import { session } from '../stores/session'

const props = defineProps({
  modelValue: { type: Object, default: () => ({}) },
  label: { type: String, required: true },
  help: { type: String, required: true },
  output: { type: Boolean, default: false },
  // The path this selection appears at inside the training container, and the
  // environment variable a script should read instead of hard-coding it.
  containerPath: { type: String, default: '' },
  envVariable: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue', 'loaded'])
const spaces = ref([])
const loading = ref(false)
const loadError = ref('')
const browserVisible = ref(false)
const loadingEntries = ref(false)
const browserError = ref('')
const browserPath = ref('')
const entries = ref([])
let refreshTimer

const model = computed({
  get: () => ({ spaceId: props.modelValue?.spaceId || props.modelValue?.space || '', relativePath: props.modelValue?.relativePath || '' }),
  set: (value) => emit('update:modelValue', { spaceId: value.spaceId || '', relativePath: value.relativePath || '' }),
})
const selectableSpaces = computed(() => spaces.value.filter((space) => props.output ? space.id === 'my-runs' : space.id !== 'workspace' && space.id !== 'my-runs'))
const selectedSpace = computed(() => spaces.value.find((space) => space.id === model.value.spaceId) || null)
const isSelected = (space) => model.value.spaceId === space.id
const accessLabel = (space) => dataSpaceAccessLabel(space, session.value)
const accessType = (space) => dataSpaceAccessType(space, session.value)
const isReady = (space) => dataSpaceReadiness(space).ready
const readinessMessage = (space) => dataSpaceReadiness(space).message
const selectionLabel = computed(() => selectedDataSpaceDirectoryLabel(selectedSpace.value?.name, model.value.relativePath))
const browserLabel = computed(() => selectedDataSpaceDirectoryLabel(selectedSpace.value?.name, browserPath.value))

const selectSpace = (space) => {
  if (!isReady(space)) return
  model.value = { spaceId: space.id, relativePath: '' }
}

const openBrowser = async () => {
  if (!selectedSpace.value?.browseEnabled) return
  browserPath.value = model.value.relativePath || ''
  browserVisible.value = true
  await loadEntries()
}

const loadEntries = async () => {
  if (!selectedSpace.value?.browseEnabled) return
  loadingEntries.value = true
  browserError.value = ''
  try {
    const page = await fetchDataSpaceEntries(selectedSpace.value.id, browserPath.value)
    entries.value = page.entries || []
  } catch (error) {
    entries.value = []
    browserError.value = error.message || '无法读取目录，请稍后重试。'
  } finally {
    loadingEntries.value = false
  }
}

const enterDirectory = async (name) => {
  try {
    browserPath.value = appendDataSpaceDirectory(browserPath.value, name)
    await loadEntries()
  } catch (error) {
    browserError.value = error.message || '目录名称不合法。'
  }
}

const goUp = async () => {
  browserPath.value = parentDataSpaceDirectory(browserPath.value)
  await loadEntries()
}

const confirmDirectory = () => {
  model.value = { spaceId: selectedSpace.value.id, relativePath: browserPath.value }
  browserVisible.value = false
}

const scheduleRefresh = () => {
  window.clearTimeout(refreshTimer)
  if (spaces.value.some((space) => space.mountStatus === 'pending')) {
    refreshTimer = window.setTimeout(loadSpaces, 5000)
  }
}

const loadSpaces = async () => {
  loading.value = true
  loadError.value = ''
  try {
    spaces.value = await fetchDataSpaces()
    if (props.output && !model.value.spaceId && spaces.value.some((space) => space.id === 'my-runs' && isReady(space))) {
      model.value = { spaceId: 'my-runs', relativePath: '' }
    }
    emit('loaded', spaces.value)
  } catch (error) {
    loadError.value = error.message || '无法加载可用数据空间，请稍后重试。'
  } finally {
    loading.value = false
    scheduleRefresh()
  }
}

onMounted(loadSpaces)
onUnmounted(() => window.clearTimeout(refreshTimer))
</script>
