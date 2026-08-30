<template>
  <section class="rounded-2xl border border-slate-800 bg-slate-950/40 p-5">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <p class="text-sm font-semibold text-slate-100">训练数据版本</p>
        <p class="mt-1 max-w-3xl text-xs leading-5 text-slate-500">
          只选择已发布的逻辑数据集。选择“最新可用”时，平台会在提交前固定为具体的不可变版本。
        </p>
      </div>
      <el-tag size="small" type="success" effect="plain">只读·版本化</el-tag>
    </div>

    <div v-if="loadingDatasets" class="mt-5 text-sm text-slate-500">正在加载可用数据集…</div>
    <el-alert v-else-if="datasetError" class="mt-5" type="warning" :closable="false">
      <template #title>{{ datasetError }}</template>
      <el-button class="mt-3" size="small" @click="loadDatasets">重试</el-button>
    </el-alert>
    <template v-else>
      <div v-if="datasets.length" class="mt-5 grid gap-4 lg:grid-cols-2">
        <label class="space-y-2">
          <span class="text-xs font-medium text-slate-300">逻辑数据集</span>
          <el-select
            v-model="selectedDatasetId"
            class="w-full"
            filterable
            clearable
            placeholder="选择已授权的数据集"
            :disabled="disabled"
          >
            <el-option
              v-for="dataset in datasets"
              :key="dataset.id"
              :label="dataset.name || dataset.slug"
              :value="dataset.id"
            >
              <div class="flex min-w-0 items-center justify-between gap-3">
                <span class="truncate">{{ dataset.name || dataset.slug }}</span>
                <span class="truncate font-mono text-xs text-slate-500">{{ dataset.slug }}</span>
              </div>
            </el-option>
          </el-select>
        </label>

        <label class="space-y-2">
          <span class="text-xs font-medium text-slate-300">数据版本</span>
          <el-select
            v-model="selectedVersionId"
            class="w-full"
            placeholder="选择 READY 版本"
            :loading="loadingVersions"
            :disabled="disabled || !selectedDatasetId || loadingVersions || !versionOptions.length"
          >
            <el-option
              v-for="option in versionOptions"
              :key="`${option.value}-${option.resolvedVersionId}`"
              :label="option.label"
              :value="option.value"
            >
              <div class="flex min-w-0 items-center justify-between gap-3">
                <span class="truncate">{{ option.label }}</span>
                <span class="font-mono text-xs text-slate-500">{{ formatDatasetCount(option.version.trainSamples) }} train</span>
              </div>
            </el-option>
          </el-select>
        </label>
      </div>

      <el-empty
        v-else
        class="mt-4"
        :image-size="72"
        description="当前账号暂无可见的版本化数据集"
      />

      <el-alert v-if="versionError" class="mt-4" type="warning" :closable="false">
        <template #title>{{ versionError }}</template>
        <el-button class="mt-3" size="small" @click="retryVersions">重试读取版本</el-button>
      </el-alert>
      <el-alert
        v-else-if="selectedDatasetId && !loadingVersions && !versionOptions.length"
        class="mt-4"
        type="info"
        :closable="false"
      >
        <template #title>该数据集还没有 READY 版本，暂时不能用于训练。</template>
      </el-alert>

      <div v-if="selectedDataset" class="mt-4 rounded-xl border border-slate-800 bg-slate-950/40 p-4">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <p class="font-medium text-slate-100">{{ selectedDataset.name || selectedDataset.slug }}</p>
            <p v-if="selectedDataset.description" class="mt-1 text-xs leading-5 text-slate-500">{{ selectedDataset.description }}</p>
          </div>
          <el-tag size="small" effect="plain">{{ visibilityLabel(selectedDataset.visibility) }}</el-tag>
        </div>

        <div v-if="selectedVersion" class="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <div class="metric-cell">
            <p class="metric-label">train / val / test</p>
            <p class="metric-value">
              {{ formatDatasetCount(selectedVersion.trainSamples) }} /
              {{ formatDatasetCount(selectedVersion.valSamples) }} /
              {{ formatDatasetCount(selectedVersion.testSamples) }}
            </p>
          </div>
          <div class="metric-cell">
            <p class="metric-label">对象数</p>
            <p class="metric-value">{{ formatDatasetCount(selectedVersion.sourceObjectCount) }}</p>
          </div>
          <div class="metric-cell">
            <p class="metric-label">逻辑 / 打包容量</p>
            <p class="metric-value">{{ formatDatasetBytes(selectedVersion.logicalBytes) }} / {{ formatDatasetBytes(selectedVersion.packedBytes) }}</p>
          </div>
          <div class="metric-cell">
            <p class="metric-label">digest 摘要</p>
            <p class="metric-value font-mono" :title="selectedVersion.manifestSha256 || ''">{{ digestSummary(selectedVersion.manifestSha256) }}</p>
          </div>
        </div>
      </div>

      <div class="mt-5">
        <div class="flex flex-wrap items-end justify-between gap-2">
          <div>
            <p class="text-xs font-medium text-slate-300">读取策略</p>
            <p class="mt-1 text-[11px] text-slate-500">不改变数据版本，只决定任务如何读取它。</p>
          </div>
          <span class="font-mono text-[11px] text-slate-600">{{ cachePolicyModel }}</span>
        </div>
        <div class="mt-3 grid gap-3 lg:grid-cols-3">
          <button
            v-for="strategy in strategies"
            :key="strategy.value"
            type="button"
            class="rounded-xl border p-4 text-left transition"
            :class="cachePolicyModel === strategy.value ? 'border-blue-400 bg-blue-950/30' : 'border-slate-800 bg-slate-900/30 hover:border-slate-600'"
            :disabled="disabled"
            @click="cachePolicyModel = strategy.value"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-sm font-medium text-slate-100">{{ strategy.label }}</span>
              <el-tag v-if="strategy.recommended" size="small" type="success" effect="plain">推荐</el-tag>
            </div>
            <p class="mt-2 text-xs leading-5 text-slate-500">{{ strategy.description }}</p>
          </button>
        </div>
      </div>
    </template>
  </section>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue'

import { fetchDatasets, fetchDatasetVersions } from '../api/datasets.js'
import {
  datasetVersionOptions,
  formatDatasetBytes,
  formatDatasetCount,
  normalizeDatasetList,
  normalizeDatasetVersions,
} from '../datasetCatalog.js'

const props = defineProps({
  modelValue: { type: Object, default: () => ({ dataset: '', version: 'latest' }) },
  cachePolicy: { type: String, default: 'auto' },
  disabled: { type: Boolean, default: false },
})

const emit = defineEmits(['update:modelValue', 'update:cachePolicy', 'loaded'])

const strategies = Object.freeze([
  Object.freeze({ value: 'auto', label: '自动', recommended: true, description: '由平台在流式读取与有界工作集之间自动选择。' }),
  Object.freeze({ value: 'off', label: '原始流式', recommended: false, description: '按需读取已发布分片，不使用节点有界数据缓存。' }),
  Object.freeze({ value: 'bounded', label: '有界缓存', recommended: false, description: '使用双盘 NVMe 工作集复用热分片，容量不需要覆盖全部数据。' }),
])
const allowedPolicies = new Set(strategies.map(({ value }) => value))

const datasets = ref([])
const versionOptions = ref([])
const loadingDatasets = ref(false)
const loadingVersions = ref(false)
const datasetError = ref('')
const versionError = ref('')
let versionRequest = 0
let loadedDatasetId = ''

const selectedDatasetId = computed({
  get: () => String(props.modelValue?.dataset || ''),
  set: (dataset) => selectDataset(String(dataset || '')),
})
const selectedVersionId = computed({
  get: () => String(props.modelValue?.version || ''),
  set: (version) => emitSelection(selectedDatasetId.value, String(version || '')),
})
const cachePolicyModel = computed({
  get: () => allowedPolicies.has(props.cachePolicy) ? props.cachePolicy : 'auto',
  set: (value) => {
    if (allowedPolicies.has(value)) emit('update:cachePolicy', value)
  },
})
const selectedDataset = computed(() => datasets.value.find(({ id }) => id === selectedDatasetId.value) || null)
const selectedVersion = computed(() => {
  const selected = versionOptions.value.find(({ value }) => value === selectedVersionId.value)
  return selected?.version || null
})

const emitSelection = (dataset, version) => {
  emit('update:modelValue', { dataset, version })
}

const digestSummary = (digest) => {
  const value = String(digest || '').trim()
  return value ? `${value.slice(0, 12)}…${value.slice(-8)}` : '—'
}
const visibilityLabel = (visibility) => visibility === 'PUBLIC' ? '全平台可见' : '本团队可见'

const loadVersions = async (datasetId, preferredVersion = 'latest') => {
  const request = ++versionRequest
  loadedDatasetId = datasetId
  versionOptions.value = []
  versionError.value = ''
  if (!datasetId) {
    loadingVersions.value = false
    return
  }
  loadingVersions.value = true
  try {
    const normalized = normalizeDatasetVersions(await fetchDatasetVersions(datasetId))
    if (request !== versionRequest) return
    const options = datasetVersionOptions(normalized)
    versionOptions.value = options
    const preferred = options.find(({ value, version }) => value === preferredVersion || version.version === preferredVersion)
    emitSelection(datasetId, preferred?.value || (options.length ? 'latest' : ''))
  } catch (error) {
    if (request !== versionRequest) return
    versionError.value = error?.message || '无法读取该数据集的版本，请稍后重试。'
    emitSelection(datasetId, '')
  } finally {
    if (request === versionRequest) loadingVersions.value = false
  }
}

const selectDataset = (datasetId) => {
  emitSelection(datasetId, datasetId ? 'latest' : '')
  void loadVersions(datasetId, 'latest')
}

const retryVersions = () => {
  void loadVersions(selectedDatasetId.value, props.modelValue?.version || 'latest')
}

const loadDatasets = async () => {
  loadingDatasets.value = true
  datasetError.value = ''
  datasets.value = []
  try {
    const normalized = normalizeDatasetList(await fetchDatasets())
    datasets.value = normalized
    emit('loaded', normalized)
    const requested = String(props.modelValue?.dataset || '')
    const matched = normalized.find(({ id, slug }) => id === requested || slug === requested)
    if (matched) {
      const preferredVersion = String(props.modelValue?.version || 'latest') || 'latest'
      emitSelection(matched.id, preferredVersion)
      await loadVersions(matched.id, preferredVersion)
    } else if (requested) {
      emitSelection('', '')
    }
  } catch (error) {
    datasetError.value = error?.message || '无法加载可用数据集，请稍后重试。'
    emitSelection('', '')
  } finally {
    loadingDatasets.value = false
  }
}

watch(
  () => props.modelValue?.dataset,
  (value) => {
    const requested = String(value || '')
    const matched = datasets.value.find(({ id, slug }) => id === requested || slug === requested)
    if (matched && matched.id !== loadedDatasetId) {
      if (requested !== matched.id) emitSelection(matched.id, props.modelValue?.version || 'latest')
      void loadVersions(matched.id, props.modelValue?.version || 'latest')
    }
  },
)

onMounted(loadDatasets)
</script>

<style scoped>
.metric-cell {
  @apply rounded-lg border border-slate-800 bg-slate-900/40 px-3 py-2;
}

.metric-label {
  @apply text-[11px] text-slate-500;
}

.metric-value {
  @apply mt-1 break-all text-xs font-medium text-slate-200;
}
</style>
