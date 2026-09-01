<template>
  <div class="mx-auto max-w-7xl space-y-6">
    <section class="flex flex-wrap items-start justify-between gap-4 rounded-2xl border border-slate-800 bg-[#131826] p-6 shadow-xl">
      <div>
        <p class="text-xs font-semibold uppercase tracking-[0.18em] text-blue-400">Governed datasets</p>
        <h1 class="mt-1 text-2xl font-bold text-white">版本化数据集</h1>
        <p class="mt-2 max-w-3xl text-sm leading-6 text-slate-400">
          从已发布的逻辑数据集中选择不可变版本。训练记录固定版本与摘要，后续数据更新不会改变已提交的任务。
        </p>
      </div>
      <el-button :loading="loading" @click="loadPage">刷新</el-button>
    </section>

    <section v-if="loading && !capabilityChecked" class="rounded-2xl border border-slate-800 bg-[#131826] p-6">
      <el-skeleton :rows="4" animated />
    </section>

    <el-alert v-else-if="capabilityError" type="warning" :closable="false" show-icon>
      <template #title>暂时无法确认数据集功能是否开放</template>
      <p class="mt-2 text-xs leading-5">为避免展示未授权的内容，页面已安全关闭数据集查询。{{ capabilityError }}</p>
    </el-alert>

    <section
      v-else-if="capabilityChecked && !datasetCapabilities.catalogEnabled"
      class="rounded-2xl border border-dashed border-slate-700 bg-[#131826] p-10 text-center"
    >
      <p class="text-base font-semibold text-slate-200">当前团队未开放版本化数据集</p>
      <p class="mt-2 text-sm text-slate-500">你仍可继续使用现有数据目录提交训练；功能开放后此页会自动显示可用版本。</p>
    </section>

    <template v-else-if="datasetCapabilities.catalogEnabled">
      <el-alert v-if="catalogError" type="warning" :closable="false" show-icon>
        <template #title>{{ catalogError }}</template>
      </el-alert>

      <section v-if="!loading && !catalogError && !datasetRows.length" class="rounded-2xl border border-dashed border-slate-700 bg-[#131826] p-10">
        <el-empty :image-size="88" description="当前账号暂无可见的版本化数据集" />
      </section>

      <section v-for="row in datasetRows" :key="row.dataset.id" class="overflow-hidden rounded-2xl border border-slate-800 bg-[#131826] shadow-xl">
        <div class="p-6">
          <div class="flex flex-wrap items-start justify-between gap-4">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h2 class="text-lg font-semibold text-white">{{ row.dataset.name || row.dataset.slug }}</h2>
                <el-tag size="small" effect="plain">{{ visibilityLabel(row.dataset.visibility) }}</el-tag>
                <el-tag size="small" effect="plain">schema {{ row.dataset.schemaVersion }}</el-tag>
              </div>
              <p class="mt-1 font-mono text-xs text-blue-300">{{ row.dataset.slug }}</p>
              <p v-if="row.dataset.description" class="mt-2 max-w-3xl text-sm leading-6 text-slate-400">{{ row.dataset.description }}</p>
            </div>
            <div class="flex flex-wrap gap-2">
              <el-button
                v-if="canPublishDataset(row.dataset)"
                :loading="publishing[row.dataset.id] === true"
                :disabled="!datasetCapabilities.publisherEnabled"
                @click="publishDataset(row.dataset)"
              >
                发布新版本
              </el-button>
              <el-button
                type="primary"
                :disabled="!row.latestReady"
                @click="createTraining(row.dataset, 'latest')"
              >
                使用最新版本训练
              </el-button>
            </div>
          </div>

          <el-alert v-if="row.versionError" class="mt-5" type="warning" :closable="false">
            <template #title>{{ row.versionError }}</template>
            <el-button class="mt-3" size="small" @click="retryDatasetVersions(row.dataset)">重试读取版本</el-button>
          </el-alert>

          <div v-else-if="row.latestReady" class="mt-5 grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <div class="summary-card">
              <p class="summary-label">最新 READY 版本</p>
              <p class="summary-value">{{ row.latestReady.version }}</p>
              <p class="mt-1 truncate font-mono text-[11px] text-slate-500" :title="row.latestReady.manifestSha256">摘要 {{ digestSummary(row.latestReady.manifestSha256) }}</p>
            </div>
            <div class="summary-card">
              <p class="summary-label">train / val / test</p>
              <p class="summary-value">
                {{ formatDatasetCount(row.latestReady.trainSamples) }} /
                {{ formatDatasetCount(row.latestReady.valSamples) }} /
                {{ formatDatasetCount(row.latestReady.testSamples) }}
              </p>
              <p class="mt-1 text-[11px] text-slate-500">{{ formatDatasetCount(row.latestReady.sourceObjectCount) }} 个对象</p>
            </div>
            <div class="summary-card">
              <p class="summary-label">逻辑 / 打包容量</p>
              <p class="summary-value">{{ formatDatasetBytes(row.latestReady.logicalBytes) }}</p>
              <p class="mt-1 text-[11px] text-slate-500">打包后 {{ formatDatasetBytes(row.latestReady.packedBytes) }}</p>
            </div>
            <div class="summary-card">
              <p class="summary-label">相对上一 READY</p>
              <p class="summary-value text-sm">{{ deltaHeadline(row.latestDelta) }}</p>
              <p class="mt-1 text-[11px] leading-5 text-slate-500">{{ deltaDetail(row.latestDelta, row.previousReady) }}</p>
            </div>
          </div>

          <div v-else-if="!row.versionError" class="mt-5 rounded-xl border border-dashed border-slate-700 bg-slate-950/30 p-5 text-sm text-slate-500">
            该数据集尚无可训练版本。当版本状态变为 READY 后，即可从此页提交。
          </div>
        </div>

        <div v-if="!row.versionError" class="border-t border-slate-800 bg-slate-950/20 px-6 py-5">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
            <div>
              <h3 class="text-sm font-semibold text-slate-200">版本记录</h3>
              <p class="mt-1 text-xs text-slate-500">只有 READY 具体版本可直接提交；其他状态仅用于了解发布进度。</p>
            </div>
            <span class="text-xs text-slate-500">{{ row.versions.length }} 个版本</span>
          </div>

          <el-table :data="row.versionRows" class="!bg-transparent text-xs" empty-text="尚无版本记录">
            <el-table-column label="版本 / 摘要" min-width="220">
              <template #default="{ row: version }">
                <p class="font-medium text-slate-200">{{ version.version }}</p>
                <p class="mt-1 font-mono text-[11px] text-slate-500" :title="version.manifestSha256 || ''">{{ digestSummary(version.manifestSha256) }}</p>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="120">
              <template #default="{ row: version }">
                <el-tag size="small" :type="statePresentation(version.state).type">{{ statePresentation(version.state).label }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="发布进度" min-width="210">
              <template #default="{ row: version }">
                <template v-if="publicationFor(version.id)">
                  <el-progress
                    :percentage="publicationPercent(publicationFor(version.id))"
                    :status="publicationFor(version.id).failedPartitions > 0 ? 'exception' : undefined"
                    :stroke-width="7"
                    class="min-w-[120px]"
                  />
                  <p class="mt-1 text-[11px] text-slate-400">
                    分区 {{ formatDatasetCount(publicationFor(version.id).completedPartitions) }} / {{ formatDatasetCount(publicationFor(version.id).totalPartitions) }}，失败 {{ formatDatasetCount(publicationFor(version.id).failedPartitions) }}
                  </p>
                </template>
                <span v-else class="text-slate-600">{{ version.state === 'READY' ? '已完成' : '等待发布器更新' }}</span>
              </template>
            </el-table-column>
            <el-table-column label="train / val / test" min-width="180">
              <template #default="{ row: version }">
                <span class="font-mono text-slate-300">
                  {{ formatDatasetCount(version.trainSamples) }} /
                  {{ formatDatasetCount(version.valSamples) }} /
                  {{ formatDatasetCount(version.testSamples) }}
                </span>
              </template>
            </el-table-column>
            <el-table-column label="对象数" width="120">
              <template #default="{ row: version }">{{ formatDatasetCount(version.sourceObjectCount) }}</template>
            </el-table-column>
            <el-table-column label="逻辑 / 打包容量" min-width="190">
              <template #default="{ row: version }">
                <span>{{ formatDatasetBytes(version.logicalBytes) }}</span>
                <span class="text-slate-600"> / </span>
                <span>{{ formatDatasetBytes(version.packedBytes) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="相对上版" min-width="180">
              <template #default="{ row: version }">
                <span v-if="version.delta" class="text-slate-400">{{ compactDelta(version.delta) }}</span>
                <span v-else class="text-slate-600">—</span>
              </template>
            </el-table-column>
            <el-table-column label="操作" width="120" fixed="right" align="right">
              <template #default="{ row: version }">
                <el-button
                  v-if="version.state === 'READY'"
                  type="primary"
                  link
                  size="small"
                  @click="createTraining(row.dataset, version.id)"
                >
                  使用此版本
                </el-button>
                <span v-else class="text-xs text-slate-600">不可提交</span>
              </template>
            </el-table-column>
          </el-table>
        </div>
      </section>
    </template>
  </div>
</template>

<script setup>
import { ElMessage, ElMessageBox } from 'element-plus'
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import { fetchDatasetPublication, fetchDatasets, fetchDatasetVersions, requestDatasetPublication } from '../../api/datasets.js'
import { fetchPlatformLimits } from '../../api/platform.js'
import { roles, session } from '../../stores/session.js'
import {
  datasetVersionDelta,
  datasetVersionPresentation,
  formatDatasetBytes,
  formatDatasetCount,
  normalizeDatasetCapabilities,
  normalizeDatasetList,
  normalizeDatasetVersions,
} from '../../datasetCatalog.js'

const router = useRouter()
const loading = ref(false)
const capabilityChecked = ref(false)
const capabilityError = ref('')
const catalogError = ref('')
const datasetCapabilities = ref(normalizeDatasetCapabilities())
const datasets = ref([])
const versionsByDataset = ref(new Map())
const publicationsByVersion = ref(new Map())
const versionErrors = ref(new Map())
const publishing = ref({})

const canPublishDataset = (dataset) => {
  if (!datasetCapabilities.value.publisherEnabled) return false
  if (roles.value.includes('SuperAdmin')) return true
  return roles.value.includes('TenantAdmin') && dataset?.visibility === 'TEAM' && dataset.ownerTenantId === session.value?.tenantId
}

const setPublishing = (datasetID, value) => {
  publishing.value = { ...publishing.value, [datasetID]: value }
}

const datasetRows = computed(() => datasets.value.map((dataset) => {
  const versions = versionsByDataset.value.get(dataset.id) || []
  const readyVersions = versions.filter(({ state }) => state === 'READY')
  const versionRows = versions.map((version) => {
    if (version.state !== 'READY') return { ...version, delta: null, publication: publicationFor(version.id) }
    const readyIndex = readyVersions.findIndex(({ id }) => id === version.id)
    const previous = readyIndex >= 0 ? readyVersions[readyIndex + 1] : null
    return { ...version, delta: previous ? datasetVersionDelta(version, previous) : null, publication: publicationFor(version.id) }
  })
  const latestReady = readyVersions[0] || null
  const previousReady = readyVersions[1] || null
  return {
    dataset,
    versions,
    versionRows,
    latestReady,
    previousReady,
    latestDelta: latestReady && previousReady ? datasetVersionDelta(latestReady, previousReady) : null,
    versionError: versionErrors.value.get(dataset.id) || '',
  }
}))

const visibilityLabel = (visibility) => visibility === 'PUBLIC' ? '全平台可见' : '本团队可见'
const statePresentation = datasetVersionPresentation
const activePublicationStates = new Set(['DISCOVERING', 'STABILIZING', 'VALIDATING', 'PACKING', 'FAILED'])
const nonNegativeCount = (value) => {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : 0
}
const normalizePublication = (value) => ({
  totalPartitions: nonNegativeCount(value?.totalPartitions),
  completedPartitions: nonNegativeCount(value?.completedPartitions),
  failedPartitions: nonNegativeCount(value?.failedPartitions),
})
const publicationFor = (versionID) => publicationsByVersion.value.get(versionID) || null
const publicationPercent = (publication) => {
  const total = nonNegativeCount(publication?.totalPartitions)
  const completed = nonNegativeCount(publication?.completedPartitions)
  return total > 0 ? Math.min(100, Math.round((completed / total) * 100)) : 0
}
const digestSummary = (digest) => {
  const value = String(digest || '').trim()
  return value ? `${value.slice(0, 12)}…${value.slice(-8)}` : '尚未生成'
}
const signedCount = (value) => {
  const number = Number(value || 0)
  if (number === 0) return '0'
  return `${number > 0 ? '+' : '−'}${formatDatasetCount(Math.abs(number))}`
}
const signedBytes = (value) => {
  const number = Number(value || 0)
  if (number === 0) return '0 B'
  return `${number > 0 ? '+' : '−'}${formatDatasetBytes(Math.abs(number))}`
}
const hasDelta = (delta) => delta && Object.values(delta).some((value) => Number(value) !== 0)
const deltaHeadline = (delta) => {
  if (!delta) return '首个可用版本'
  if (!hasDelta(delta)) return '无数量变化'
  return `train ${signedCount(delta.trainSamples)} · 对象 ${signedCount(delta.sourceObjectCount)}`
}
const deltaDetail = (delta, previous) => {
  if (!previous) return '暂无上一个 READY 版本可比较。'
  return `val ${signedCount(delta.valSamples)} · test ${signedCount(delta.testSamples)} · 逻辑容量 ${signedBytes(delta.logicalBytes)} · 打包容量 ${signedBytes(delta.packedBytes)}`
}
const compactDelta = (delta) => hasDelta(delta)
  ? `train ${signedCount(delta.trainSamples)} · 对象 ${signedCount(delta.sourceObjectCount)} · ${signedBytes(delta.packedBytes)}`
  : '无数量变化'

const fetchVersionsForDataset = async (dataset) => {
  try {
    const versions = normalizeDatasetVersions(await fetchDatasetVersions(dataset.id))
    const publications = await Promise.all(versions
      .filter((version) => activePublicationStates.has(version.state))
      .map(async (version) => {
        try {
          return [version.id, normalizePublication(await fetchDatasetPublication(dataset.id, version.id))]
        } catch {
          return [version.id, null]
        }
      }))
    return { datasetId: dataset.id, versions, publications, error: '' }
  } catch (error) {
    return { datasetId: dataset.id, versions: [], publications: [], error: error?.message || '无法读取版本，请稍后重试。' }
  }
}

const retryDatasetVersions = async (dataset) => {
  const result = await fetchVersionsForDataset(dataset)
  const nextVersions = new Map(versionsByDataset.value)
  const nextPublications = new Map(publicationsByVersion.value)
  const nextErrors = new Map(versionErrors.value)
  nextVersions.set(result.datasetId, result.versions)
  for (const [versionID, publication] of result.publications) {
    if (publication) nextPublications.set(versionID, publication)
    else nextPublications.delete(versionID)
  }
  if (result.error) nextErrors.set(result.datasetId, result.error)
  else nextErrors.delete(result.datasetId)
  versionsByDataset.value = nextVersions
  publicationsByVersion.value = nextPublications
  versionErrors.value = nextErrors
}

const loadPage = async () => {
  loading.value = true
  capabilityChecked.value = false
  capabilityError.value = ''
  catalogError.value = ''
  datasetCapabilities.value = normalizeDatasetCapabilities()
  datasets.value = []
  versionsByDataset.value = new Map()
  publicationsByVersion.value = new Map()
  versionErrors.value = new Map()

  try {
    let limits
    try {
      limits = await fetchPlatformLimits()
    } catch (error) {
      capabilityError.value = error?.message || '请稍后刷新重试。'
      return
    } finally {
      capabilityChecked.value = true
    }

    datasetCapabilities.value = normalizeDatasetCapabilities(limits?.datasets)
    if (!datasetCapabilities.value.catalogEnabled) return

    let visibleDatasets
    try {
      visibleDatasets = normalizeDatasetList(await fetchDatasets())
    } catch (error) {
      catalogError.value = error?.message || '无法加载数据集目录，请稍后重试。'
      return
    }

    datasets.value = visibleDatasets
    const results = await Promise.all(visibleDatasets.map(fetchVersionsForDataset))
    versionsByDataset.value = new Map(results.map(({ datasetId, versions }) => [datasetId, versions]))
    publicationsByVersion.value = new Map(results.flatMap(({ publications }) => publications.filter(([, publication]) => publication)))
    versionErrors.value = new Map(results.filter(({ error }) => error).map(({ datasetId, error }) => [datasetId, error]))
  } finally {
    loading.value = false
  }
}

const createTraining = (dataset, version) => router.push({
  path: '/job/create',
  query: {
    dataMode: 'streaming',
    dataset: dataset.id,
    datasetVersion: version,
    cachePolicy: 'auto',
  },
})

const publishDataset = async (dataset) => {
  if (!canPublishDataset(dataset)) return
  try {
    await ElMessageBox.confirm(
      `将从「${dataset.name || dataset.slug}」创建新的不可变版本。是否继续？`,
      '发布新版本',
      { confirmButtonText: '开始发布', cancelButtonText: '取消', type: 'warning' },
    )
  } catch {
    return
  }
  setPublishing(dataset.id, true)
  try {
    await requestDatasetPublication(dataset.id)
    ElMessage.success('发布请求已受理，可在版本记录中查看进度')
    await loadPage()
  } catch (error) {
    ElMessage.error(error?.message || '发布新版本失败，请稍后重试')
  } finally {
    setPublishing(dataset.id, false)
  }
}

onMounted(loadPage)
</script>

<style scoped>
.summary-card {
  @apply rounded-xl border border-slate-800 bg-slate-950/40 p-4;
}

.summary-label {
  @apply text-[11px] font-medium uppercase tracking-wide text-slate-500;
}

.summary-value {
  @apply mt-2 break-all text-base font-semibold text-slate-100;
}
</style>
