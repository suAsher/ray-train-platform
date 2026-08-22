<template>
  <div class="rounded-2xl border border-slate-800 bg-slate-950/40 p-4">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <p class="text-sm font-semibold text-slate-100">{{ label }}</p>
        <p class="mt-1 text-xs leading-5 text-slate-500">{{ help }}</p>
      </div>
      <el-tag v-if="selectedAsset" :type="selectedAsset.readOnly ? 'success' : 'warning'" effect="plain">
        {{ selectedAsset.readOnly ? '只读' : '可写' }}
      </el-tag>
    </div>

    <el-select
      v-model="selectedAssetID"
      class="mt-4 w-full"
      clearable
      filterable
      :loading="loadingAssets"
      placeholder="选择已授权的存储位置"
    >
      <el-option v-for="asset in assets" :key="asset.id" :value="asset.id" :label="asset.name">
        <div class="flex items-center justify-between gap-3">
          <span class="font-medium">{{ asset.name }}</span>
          <span class="text-xs text-slate-500">{{ providerLabel(asset.provider) }} · {{ asset.readOnly ? '只读' : '可写' }}</span>
        </div>
      </el-option>
    </el-select>

    <p v-if="assetsError" class="mt-3 text-xs text-rose-300">{{ assetsError }}</p>
    <p v-else-if="!loadingAssets && assets.length === 0" class="mt-3 text-xs leading-5 text-slate-500">
      暂无可用存储目录。请联系租户管理员登记已挂载的数据根目录。
    </p>

    <template v-if="selectedAsset">
      <div v-if="output" class="mt-4 rounded-xl border border-amber-500/30 bg-amber-950/20 p-3 text-xs leading-5 text-amber-100/80">
        平台会在该存储位置下自动创建本任务专属的 <code>runs/&lt;job-id&gt;</code> 目录。无需填写路径，也不会覆盖已有产物。
      </div>

      <div v-else-if="canBrowse" class="mt-4 rounded-xl border border-slate-800 bg-slate-900/60 p-3">
        <div class="flex flex-wrap items-center gap-1 text-xs">
          <el-button text size="small" :disabled="!selectedPath" @click="goToPath('')">{{ selectedAsset.name }}</el-button>
          <template v-for="(segment, index) in breadcrumbs" :key="`${index}-${segment}`">
            <span class="text-slate-600">/</span>
            <el-button text size="small" :disabled="index === breadcrumbs.length - 1" @click="goToPath(breadcrumbs.slice(0, index + 1).join('/'))">{{ segment }}</el-button>
          </template>
        </div>

        <div class="mt-3 flex flex-wrap gap-2">
          <el-button size="small" :type="selectedPath ? 'primary' : 'default'" plain @click="chooseCurrentDirectory">
            使用当前目录{{ selectedPath ? `：${selectedPath}` : '（根目录）' }}
          </el-button>
          <el-button v-if="nextCursor" size="small" :loading="loadingDirectories" @click="loadDirectories(nextCursor, true)">加载更多</el-button>
        </div>

        <div v-if="loadingDirectories" class="mt-3 text-xs text-slate-500">正在加载已授权子目录…</div>
        <p v-else-if="directoriesError" class="mt-3 text-xs text-rose-300">{{ directoriesError }}</p>
        <div v-else-if="directories.length" class="mt-3 grid gap-2 sm:grid-cols-2">
          <button
            v-for="directory in directories"
            :key="directory"
            type="button"
            class="flex items-center justify-between rounded-lg border border-slate-700 bg-slate-950 px-3 py-2 text-left text-xs text-slate-200 transition hover:border-blue-400 hover:text-blue-200"
            @click="enterDirectory(directory)"
          >
            <span class="truncate">{{ directory }}</span>
            <span class="ml-2 text-slate-500">进入 ›</span>
          </button>
        </div>
        <p v-else class="mt-3 text-xs text-slate-500">当前目录没有可浏览的下级目录，可以直接选择它。</p>
      </div>

      <div v-else class="mt-4 rounded-xl border border-slate-800 bg-slate-900/60 p-3 text-xs leading-5 text-slate-400">
        此存储根目录由平台挂载并授权，当前不提供目录浏览；选择后训练任务会直接使用该目录。
      </div>
    </template>
  </div>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue'

import { fetchStorageAssets, fetchStorageDirectories } from '../api/storageAssets'

const props = defineProps({
  modelValue: { type: Object, default: () => ({}) },
  kind: { type: String, required: true },
  label: { type: String, required: true },
  help: { type: String, default: '' },
  output: { type: Boolean, default: false },
})

const emit = defineEmits(['update:modelValue'])
const assets = ref([])
const loadingAssets = ref(false)
const assetsError = ref('')
const directories = ref([])
const loadingDirectories = ref(false)
const directoriesError = ref('')
const nextCursor = ref('')
let requestSequence = 0

const selectedAssetID = computed({
  get: () => props.modelValue?.assetId || '',
  set: (assetID) => selectAsset(assetID),
})
const selectedPath = computed(() => props.modelValue?.relativePath || '')
const selectedAsset = computed(() => assets.value.find((asset) => asset.id === selectedAssetID.value) || null)
const breadcrumbs = computed(() => selectedPath.value ? selectedPath.value.split('/') : [])
const canBrowse = computed(() => !props.output && selectedAsset.value?.provider === 'tos' && selectedAsset.value?.browseEnabled)

const providerLabel = (provider) => provider === 'idc' ? 'IDC 只读存储' : 'TOS 对象存储'

const loadAssets = async () => {
  loadingAssets.value = true
  assetsError.value = ''
  try {
    assets.value = await fetchStorageAssets(props.kind)
    if (selectedAssetID.value && !selectedAsset.value) emit('update:modelValue', {})
  } catch (error) {
    assets.value = []
    assetsError.value = error.message || '无法加载存储目录册'
  } finally {
    loadingAssets.value = false
  }
}

const selectAsset = (assetID) => {
  directories.value = []
  nextCursor.value = ''
  directoriesError.value = ''
  if (!assetID) {
    emit('update:modelValue', {})
    return
  }
  emit('update:modelValue', { assetId: assetID })
  if (!props.output) loadDirectories()
}

const chooseCurrentDirectory = () => {
  if (!selectedAssetID.value) return
  emit('update:modelValue', selectedPath.value ? { assetId: selectedAssetID.value, relativePath: selectedPath.value } : { assetId: selectedAssetID.value })
}

const goToPath = (path) => {
  if (!selectedAssetID.value) return
  emit('update:modelValue', path ? { assetId: selectedAssetID.value, relativePath: path } : { assetId: selectedAssetID.value })
  loadDirectories()
}

const enterDirectory = (directory) => {
  const path = [selectedPath.value, directory].filter(Boolean).join('/')
  goToPath(path)
}

const loadDirectories = async (cursor = '', append = false) => {
  if (!canBrowse.value || !selectedAssetID.value) return
  const sequence = ++requestSequence
  loadingDirectories.value = true
  directoriesError.value = ''
  try {
    const page = await fetchStorageDirectories(selectedAssetID.value, selectedPath.value, cursor)
    if (sequence !== requestSequence) return
    directories.value = append ? [...directories.value, ...(page.directories || [])] : (page.directories || [])
    nextCursor.value = page.nextCursor || ''
  } catch (error) {
    if (sequence !== requestSequence) return
    directoriesError.value = error.message || '无法浏览当前存储目录'
    directories.value = append ? directories.value : []
    nextCursor.value = ''
  } finally {
    if (sequence === requestSequence) loadingDirectories.value = false
  }
}

watch(() => props.kind, () => loadAssets())
onMounted(loadAssets)
</script>

<style scoped>
code {
  color: rgb(253 230 138);
}
</style>
