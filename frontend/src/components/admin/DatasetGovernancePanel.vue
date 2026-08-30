<template>
  <div class="space-y-4">
    <el-alert
      v-if="!datasetCapabilities.catalogEnabled"
      type="info"
      :closable="false"
      show-icon
      title="版本化数据集治理未启用"
    >
      当前部署没有开放数据集目录。此处不会读取或修改数据集。
    </el-alert>

    <el-alert
      v-else-if="!canManageCatalog"
      type="warning"
      :closable="false"
      show-icon
      title="需要团队管理员权限"
    >
      请使用 TenantAdmin 或 SuperAdmin 账号管理数据集。
    </el-alert>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h4 class="text-sm font-bold text-white">版本化数据集治理</h4>
          <p class="mt-1 max-w-3xl text-xs leading-5 text-slate-400">
            把团队或公共数据空间中的逻辑目录登记为数据集，再发布不可变版本。
            用户提交训练时只选数据集与版本。
          </p>
        </div>
        <div class="flex flex-wrap gap-2">
          <el-button size="small" :loading="loading" @click="loadDatasets">刷新</el-button>
          <el-button
            v-if="effectiveSuperAdmin"
            size="small"
            :disabled="!datasetCapabilities.publisherEnabled"
            :loading="gcLoading"
            @click="previewGarbageCollection"
          >
            回收预览
          </el-button>
          <el-button type="primary" size="small" @click="openCreateDialog">创建数据集</el-button>
        </div>
      </div>

      <el-alert
        v-if="!datasetCapabilities.publisherEnabled"
        type="warning"
        :closable="false"
        show-icon
        title="数据集发布器未启用"
      >
        可查看和创建数据集定义，但不能发布新版本或运行回收预览。
      </el-alert>

      <el-alert
        v-if="catalogError"
        type="error"
        :closable="false"
        show-icon
        :title="catalogError"
      >
        <el-button size="small" @click="loadDatasets">重试</el-button>
      </el-alert>

      <el-table
        v-loading="loading"
        :data="datasets"
        row-key="id"
        class="!bg-transparent text-xs"
        empty-text="尚未创建数据集"
      >
        <el-table-column type="expand">
          <template #default="scope">
            <div class="space-y-3 px-4 py-2">
              <div
                v-if="currentPublication(scope.row.id)"
                class="rounded-xl border border-blue-500/20 bg-blue-500/5 p-3"
              >
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <p class="text-xs font-semibold text-blue-200">当前会话发布状态</p>
                    <p class="mt-1 text-[11px] leading-5 text-slate-400">
                      仅展示本页打开后发起的请求；平台目前没有可查询的历史发布列表。
                    </p>
                  </div>
                  <el-button size="small" :loading="versionLoading[scope.row.id] === true" @click="loadDatasetVersions(scope.row.id)">
                    刷新版本状态
                  </el-button>
                </div>
                <div class="mt-3 flex flex-wrap items-center gap-2 text-[11px] text-slate-300">
                  <el-tag :type="versionState(currentPublicationState(scope.row.id)).type" size="small" effect="plain">
                    {{ versionState(currentPublicationState(scope.row.id)).label }}
                  </el-tag>
                  <span>版本 {{ currentPublication(scope.row.id).datasetVersionId }}</span>
                  <span>受理时 {{ formatAcceptedAt(currentPublication(scope.row.id).acceptedAt) }}</span>
                  <span>受理时计数：已处理 {{ formatDatasetCount(currentPublication(scope.row.id).processedObjectCount) }} / {{ formatDatasetCount(currentPublication(scope.row.id).sourceObjectCount) }}，失败 {{ formatDatasetCount(currentPublication(scope.row.id).failedObjectCount) }}</span>
                </div>
              </div>

              <el-alert
                v-if="versionErrors[scope.row.id]"
                type="error"
                :closable="false"
                show-icon
                :title="versionErrors[scope.row.id]"
              />

              <el-table
                v-loading="versionLoading[scope.row.id] === true"
                :data="versionsFor(scope.row.id)"
                size="small"
                class="!bg-transparent text-xs"
                empty-text="尚未发布任何版本"
              >
                <el-table-column prop="version" label="版本" min-width="150" />
                <el-table-column label="状态" width="105">
                  <template #default="versionScope">
                    <el-tag :type="versionState(versionScope.row.state).type" size="small" effect="plain">
                      {{ versionState(versionScope.row.state).label }}
                    </el-tag>
                  </template>
                </el-table-column>
                <el-table-column label="样本" min-width="190">
                  <template #default="versionScope">
                    train {{ formatDatasetCount(versionScope.row.trainSamples) }} ·
                    val {{ formatDatasetCount(versionScope.row.valSamples) }} ·
                    test {{ formatDatasetCount(versionScope.row.testSamples) }}
                  </template>
                </el-table-column>
                <el-table-column label="对象数" width="105">
                  <template #default="versionScope">{{ formatDatasetCount(versionScope.row.sourceObjectCount) }}</template>
                </el-table-column>
                <el-table-column label="逻辑 / 打包容量" min-width="170">
                  <template #default="versionScope">
                    {{ formatDatasetBytes(versionScope.row.logicalBytes) }} / {{ formatDatasetBytes(versionScope.row.packedBytes) }}
                  </template>
                </el-table-column>
                <el-table-column label="摘要" min-width="125">
                  <template #default="versionScope">
                    <span class="font-mono text-[11px] text-slate-400">{{ shortDigest(versionScope.row.manifestSha256) }}</span>
                  </template>
                </el-table-column>
                <el-table-column label="操作" width="110" align="right">
                  <template #default="versionScope">
                    <el-button
                      v-if="versionScope.row.state === 'READY' && canManageDataset(scope.row)"
                      type="danger"
                      link
                      size="small"
                      :loading="deprecating[versionScope.row.id] === true"
                      @click="deprecateVersion(scope.row, versionScope.row)"
                    >
                      弃用版本
                    </el-button>
                  </template>
                </el-table-column>
              </el-table>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="name" label="数据集" min-width="170">
          <template #default="scope">
            <div>
              <p class="font-semibold text-slate-100">{{ scope.row.name }}</p>
              <p class="mt-1 font-mono text-[11px] text-slate-500">{{ scope.row.slug }}</p>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="范围" width="105">
          <template #default="scope">
            <el-tag :type="scope.row.visibility === 'PUBLIC' ? 'success' : 'warning'" size="small" effect="plain">
              {{ scope.row.visibility === 'PUBLIC' ? '全平台' : '本团队' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="来源" min-width="210">
          <template #default="scope">
            <p>{{ sourceSpaceLabel(scope.row.sourceSpace) }}</p>
            <p class="mt-1 break-all font-mono text-[11px] text-slate-500">{{ scope.row.sourceRelativePath }}</p>
          </template>
        </el-table-column>
        <el-table-column prop="schemaVersion" label="Schema" min-width="120" />
        <el-table-column label="版本概况" min-width="155">
          <template #default="scope">
            <span v-if="versionsFor(scope.row.id).length">
              {{ versionsFor(scope.row.id).length }} 个，
              {{ readyVersionCount(scope.row.id) }} 个可训练
            </span>
            <span v-else class="text-slate-500">暂无版本</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="130" align="right">
          <template #default="scope">
            <el-button
              v-if="canManageDataset(scope.row)"
              type="primary"
              link
              size="small"
              :disabled="!datasetCapabilities.publisherEnabled"
              :loading="publishing[scope.row.id] === true"
              @click="publishDataset(scope.row)"
            >
              发布新版本
            </el-button>
            <span v-else class="text-[11px] text-slate-500">只读查看</span>
          </template>
        </el-table-column>
      </el-table>
    </template>

    <el-dialog v-model="createDialogVisible" title="创建数据集" width="min(560px, 94vw)" @closed="resetCreateForm">
      <el-form label-position="top" @submit.prevent>
        <div class="grid gap-3 md:grid-cols-2">
          <el-form-item label="数据集标识">
            <el-input v-model="createForm.slug" maxlength="64" placeholder="例如 s1h-labeled" />
            <p class="field-help">仅小写字母、数字和短横线，创建后不变。</p>
          </el-form-item>
          <el-form-item label="显示名称">
            <el-input v-model="createForm.name" maxlength="128" placeholder="例如 S1H 全量标注" />
          </el-form-item>
        </div>

        <el-form-item label="说明">
          <el-input v-model="createForm.description" type="textarea" :rows="2" maxlength="1000" show-word-limit placeholder="可选，说明数据范围和用途" />
        </el-form-item>

        <div class="grid gap-3 md:grid-cols-2">
          <el-form-item label="可见范围">
            <el-select v-model="createForm.visibility" class="w-full" @change="syncSourceSpace">
              <el-option label="本团队（TEAM）" value="TEAM" />
              <el-option v-if="effectiveSuperAdmin" label="全平台（PUBLIC）" value="PUBLIC" />
            </el-select>
          </el-form-item>
          <el-form-item label="逻辑数据空间">
            <el-select v-model="createForm.sourceSpace" class="w-full" disabled>
              <el-option label="团队共享数据" value="team-shared" />
              <el-option label="公共数据" value="public" />
            </el-select>
            <p class="field-help">由可见范围确定，不能手动跨空间。</p>
          </el-form-item>
        </div>

        <el-form-item label="来源相对目录">
          <el-input v-model="createForm.sourceRelativePath" maxlength="4096" placeholder="例如 labeled/s1h" />
          <p class="field-help">相对于上方逻辑数据空间；不接受绝对路径或网络地址。</p>
        </el-form-item>

        <el-form-item label="Schema 版本">
          <el-input v-model="createForm.schemaVersion" maxlength="128" placeholder="例如 s1h-v1" />
          <p class="field-help">用于识别解析规则，例如 s1h-v1 或 parquet-v1。</p>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="submitCreateDataset">确认创建</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="gcDialogVisible" title="数据集版本回收预览" width="min(760px, 94vw)">
      <el-alert type="info" :closable="false" show-icon title="这是 dry-run，不会删除任何内容">
        当前共有 {{ gcPreview.count }} 个候选版本。本页只展示预览结果，不提供真正回收操作。
      </el-alert>
      <el-table :data="gcPreview.candidates" size="small" class="mt-4 !bg-transparent text-xs" empty-text="没有可回收的版本">
        <el-table-column label="数据集" min-width="180">
          <template #default="scope">{{ datasetLabel(scope.row.datasetId) }}</template>
        </el-table-column>
        <el-table-column prop="version" label="版本" min-width="150" />
        <el-table-column label="状态" width="105">
          <template #default="scope">
            <el-tag :type="versionState(scope.row.state).type" size="small" effect="plain">{{ versionState(scope.row.state).label }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="逻辑 / 打包容量" min-width="190">
          <template #default="scope">{{ formatDatasetBytes(scope.row.logicalBytes) }} / {{ formatDatasetBytes(scope.row.packedBytes) }}</template>
        </el-table-column>
      </el-table>
      <template #footer><el-button @click="gcDialogVisible = false">关闭</el-button></template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import {
  createDataset,
  deprecateDatasetVersion,
  fetchDatasets,
  fetchDatasetVersions,
  previewDatasetGarbageCollection,
  requestDatasetPublication,
} from '../../api/datasets.js'
import {
  datasetVersionPresentation,
  formatDatasetBytes,
  formatDatasetCount,
  normalizeDatasetCapabilities,
  normalizeDatasetList,
  normalizeDatasetVersions,
} from '../../datasetCatalog.js'
import { roles, session } from '../../stores/session.js'

const props = defineProps({
  capabilities: { type: Object, default: () => ({}) },
  isSuperAdmin: { type: Boolean, default: false },
})

const datasetCapabilities = computed(() => normalizeDatasetCapabilities(props.capabilities))
const hasSuperAdminRole = computed(() => roles.value.includes('SuperAdmin'))
const hasTenantAdminRole = computed(() => roles.value.includes('TenantAdmin'))
// Requiring both the server-derived role and the parent console's role decision
// prevents a missing or stale prop from accidentally widening the UI.
const effectiveSuperAdmin = computed(() => hasSuperAdminRole.value && props.isSuperAdmin)
const canManageCatalog = computed(() => effectiveSuperAdmin.value || hasTenantAdminRole.value)

const datasets = ref([])
const versionsByDataset = ref({})
const versionErrors = ref({})
const versionLoading = ref({})
const currentPublications = ref({})
const publishing = ref({})
const deprecating = ref({})
const loading = ref(false)
const catalogError = ref('')
const creating = ref(false)
const createDialogVisible = ref(false)
const gcLoading = ref(false)
const gcDialogVisible = ref(false)
const gcPreview = ref({ count: 0, candidates: [] })

const emptyCreateForm = () => ({
  slug: '',
  name: '',
  description: '',
  visibility: 'TEAM',
  sourceSpace: 'team-shared',
  sourceRelativePath: '',
  schemaVersion: '',
})
const createForm = ref(emptyCreateForm())

const safeErrorCopy = Object.freeze({
  AUTH_REQUIRED: '登录已失效，请重新登录',
  FORBIDDEN: '当前账号没有此操作权限',
  PUBLIC_DATASET_FORBIDDEN: '只有超级管理员可以创建全平台数据集',
  DATASET_MANAGEMENT_FORBIDDEN: '只能管理当前团队的数据集',
  DATASET_CONFLICT: '同一范围内已存在相同的数据集标识',
  INVALID_DATASET: '数据集定义不符合平台规则',
  INVALID_DATASET_SOURCE: '来源目录不在选定的逻辑数据空间内',
  DATASET_PUBLISHER_UNAVAILABLE: '数据集发布器当前不可用',
  DATASET_PUBLICATION_FAILED: '未能受理数据集发布请求',
  DATASET_VERSION_STATE_CONFLICT: '只有 READY 版本可以弃用',
  DATASET_GC_PREVIEW_FAILED: '无法生成数据集回收预览',
})

const safeErrorMessage = (error, fallback) => safeErrorCopy[error?.code] || fallback
const isDialogCancel = (error) => error === 'cancel' || error === 'close' || error?.message === 'cancel' || error?.message === 'close'
const versionState = (state) => datasetVersionPresentation(state)
const sourceSpaceLabel = (space) => ({ 'team-shared': '团队共享数据', public: '公共数据' }[space] || '未知空间')
const shortDigest = (digest) => digest ? `${digest.slice(0, 12)}…` : '—'
const nonNegativeCount = (value) => {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : 0
}
const formatAcceptedAt = (value) => {
  const parsed = Date.parse(value)
  return Number.isFinite(parsed) ? new Date(parsed).toLocaleString('zh-CN', { hour12: false }) : '—'
}
const versionsFor = (datasetId) => versionsByDataset.value[datasetId] || []
const readyVersionCount = (datasetId) => versionsFor(datasetId).filter(({ state }) => state === 'READY').length
const currentPublication = (datasetId) => currentPublications.value[datasetId] || null
const datasetLabel = (datasetId) => datasets.value.find(({ id }) => id === datasetId)?.name || datasetId

const currentPublicationState = (datasetId) => {
  const publication = currentPublication(datasetId)
  if (!publication) return ''
  return versionsFor(datasetId).find(({ id }) => id === publication.datasetVersionId)?.state || publication.state
}

const setBooleanFlag = (target, key, enabled) => {
  target.value = { ...target.value, [key]: enabled }
}

const clearCatalogState = () => {
  datasets.value = []
  versionsByDataset.value = {}
  versionErrors.value = {}
  versionLoading.value = {}
  currentPublications.value = {}
  catalogError.value = ''
  createDialogVisible.value = false
  gcDialogVisible.value = false
}

let loadGeneration = 0

const loadDatasetVersions = async (datasetId, generation = loadGeneration) => {
  if (!datasetCapabilities.value.catalogEnabled || !canManageCatalog.value || !datasetId) return
  setBooleanFlag(versionLoading, datasetId, true)
  versionErrors.value = { ...versionErrors.value, [datasetId]: '' }
  try {
    const versions = normalizeDatasetVersions(await fetchDatasetVersions(datasetId))
    if (generation !== loadGeneration || !datasetCapabilities.value.catalogEnabled) return
    versionsByDataset.value = { ...versionsByDataset.value, [datasetId]: versions }
  } catch (error) {
    if (generation !== loadGeneration) return
    versionsByDataset.value = { ...versionsByDataset.value, [datasetId]: [] }
    versionErrors.value = {
      ...versionErrors.value,
      [datasetId]: safeErrorMessage(error, '无法读取该数据集的版本'),
    }
  } finally {
    if (generation === loadGeneration) setBooleanFlag(versionLoading, datasetId, false)
  }
}

const loadDatasets = async () => {
  if (!datasetCapabilities.value.catalogEnabled || !canManageCatalog.value) return
  const generation = ++loadGeneration
  loading.value = true
  catalogError.value = ''
  try {
    const items = normalizeDatasetList(await fetchDatasets())
    if (generation !== loadGeneration || !datasetCapabilities.value.catalogEnabled) return
    datasets.value = items
    const activeIds = new Set(items.map(({ id }) => id))
    versionsByDataset.value = Object.fromEntries(Object.entries(versionsByDataset.value).filter(([id]) => activeIds.has(id)))
    await Promise.all(items.map(({ id }) => loadDatasetVersions(id, generation)))
  } catch (error) {
    if (generation !== loadGeneration) return
    datasets.value = []
    versionsByDataset.value = {}
    catalogError.value = safeErrorMessage(error, '无法读取数据集目录')
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

const canManageDataset = (dataset) => {
  if (!canManageCatalog.value || !dataset) return false
  if (effectiveSuperAdmin.value) return true
  return dataset.visibility === 'TEAM' && dataset.ownerTenantId === (session.value?.tenantId || '')
}

const syncSourceSpace = () => {
  if (!effectiveSuperAdmin.value && createForm.value.visibility === 'PUBLIC') {
    createForm.value = { ...createForm.value, visibility: 'TEAM', sourceSpace: 'team-shared' }
    return
  }
  createForm.value = {
    ...createForm.value,
    sourceSpace: createForm.value.visibility === 'PUBLIC' ? 'public' : 'team-shared',
  }
}

const resetCreateForm = () => {
  createForm.value = emptyCreateForm()
}

const openCreateDialog = () => {
  resetCreateForm()
  createDialogVisible.value = true
}

const validateCreateForm = () => {
  const value = createForm.value
  const slugPattern = /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/
  const schemaPattern = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/
  const controlPattern = /[\u0000-\u001f\u007f]/
  const segments = value.sourceRelativePath.split('/')

  if (!slugPattern.test(value.slug) || value.slug === '.' || value.slug === '..') return '数据集标识格式不正确'
  if (!value.name.trim() || value.name !== value.name.trim() || value.name.length > 128 || controlPattern.test(value.name)) return '请填写有效的显示名称'
  if (value.description.length > 1000 || controlPattern.test(value.description)) return '数据集说明包含无效字符'
  if (!['TEAM', 'PUBLIC'].includes(value.visibility)) return '请选择有效的可见范围'
  if (value.visibility === 'PUBLIC' && !effectiveSuperAdmin.value) return '只有超级管理员可以创建全平台数据集'
  if (value.sourceSpace !== (value.visibility === 'PUBLIC' ? 'public' : 'team-shared')) return '逻辑数据空间与可见范围不一致'
  if (!value.sourceRelativePath || value.sourceRelativePath !== value.sourceRelativePath.trim() || value.sourceRelativePath.length > 4096) return '请填写有效的来源相对目录'
  if (value.sourceRelativePath.startsWith('/') || value.sourceRelativePath.includes('\\') || value.sourceRelativePath.includes('://') || /[?#%]/.test(value.sourceRelativePath) || controlPattern.test(value.sourceRelativePath)) return '来源目录必须是安全的相对目录'
  if (segments.some((segment) => !segment || segment === '.' || segment === '..')) return '来源目录不能包含空层级或跳级符号'
  if (!schemaPattern.test(value.schemaVersion)) return 'Schema 版本格式不正确'
  return ''
}

const submitCreateDataset = async () => {
  if (!datasetCapabilities.value.catalogEnabled || !canManageCatalog.value) return
  const validationError = validateCreateForm()
  if (validationError) {
    ElMessage.warning(validationError)
    return
  }
  const scopeLabel = createForm.value.visibility === 'PUBLIC' ? '全平台' : '本团队'
  try {
    await ElMessageBox.confirm(
      `确认创建「${createForm.value.name}」并对${scopeLabel}可见？创建后数据集标识不可变更。`,
      '创建数据集',
      { confirmButtonText: '创建', cancelButtonText: '取消', type: createForm.value.visibility === 'PUBLIC' ? 'warning' : 'info' },
    )
  } catch (error) {
    if (isDialogCancel(error)) return
    ElMessage.error('无法完成创建确认')
    return
  }
  if (!datasetCapabilities.value.catalogEnabled || !canManageCatalog.value) {
    ElMessage.warning('数据集治理能力或当前权限已变更，请刷新后重试')
    return
  }

  creating.value = true
  try {
    // The API derives sourceSpace and ownership from visibility plus the
    // authenticated principal; the browser never supplies either boundary.
    await createDataset({
      slug: createForm.value.slug,
      name: createForm.value.name,
      description: createForm.value.description,
      visibility: createForm.value.visibility,
      sourceRelativePath: createForm.value.sourceRelativePath,
      schemaVersion: createForm.value.schemaVersion,
    })
    ElMessage.success(`数据集「${createForm.value.name}」已创建`)
    createDialogVisible.value = false
    resetCreateForm()
    await loadDatasets()
  } catch (error) {
    ElMessage.error(safeErrorMessage(error, '创建数据集失败'))
  } finally {
    creating.value = false
  }
}

const publishDataset = async (dataset) => {
  if (!datasetCapabilities.value.publisherEnabled) {
    ElMessage.warning('数据集发布器未启用')
    return
  }
  if (!canManageDataset(dataset)) {
    ElMessage.error('当前账号不能管理该数据集')
    return
  }
  try {
    await ElMessageBox.confirm(
      `将扫描「${dataset.name}」的当前来源目录并生成一个新的不可变版本。是否继续？`,
      '发布新版本',
      { confirmButtonText: '开始发布', cancelButtonText: '取消', type: 'warning' },
    )
  } catch (error) {
    if (isDialogCancel(error)) return
    ElMessage.error('无法完成发布确认')
    return
  }
  if (!datasetCapabilities.value.publisherEnabled || !canManageDataset(dataset)) {
    ElMessage.warning('发布能力或当前权限已变更，请刷新后重试')
    return
  }

  setBooleanFlag(publishing, dataset.id, true)
  try {
    const response = await requestDatasetPublication(dataset.id)
    const publicationId = String(response?.id || '').trim()
    const datasetVersionId = String(response?.datasetVersionId || '').trim()
    if (!publicationId || !datasetVersionId) throw new Error('invalid publication receipt')
    const publication = {
      id: publicationId,
      datasetVersionId,
      state: String(response?.state || 'DISCOVERING').toUpperCase(),
      sourceObjectCount: nonNegativeCount(response?.sourceObjectCount),
      processedObjectCount: nonNegativeCount(response?.processedObjectCount),
      failedObjectCount: nonNegativeCount(response?.failedObjectCount),
      acceptedAt: new Date().toISOString(),
    }
    currentPublications.value = { ...currentPublications.value, [dataset.id]: publication }
    ElMessage.success('发布请求已受理，请通过版本状态跟踪结果')
    await loadDatasetVersions(dataset.id)
  } catch (error) {
    ElMessage.error(safeErrorMessage(error, '发布新版本失败'))
  } finally {
    setBooleanFlag(publishing, dataset.id, false)
  }
}

const deprecateVersion = async (dataset, version) => {
  if (!canManageDataset(dataset) || version?.state !== 'READY') return
  try {
    await ElMessageBox.confirm(
      `弃用版本 ${version.version} 后，新任务不再把它作为最新可用版本；已提交任务不受影响。`,
      '弃用版本',
      { confirmButtonText: '确认弃用', cancelButtonText: '取消', type: 'warning' },
    )
  } catch (error) {
    if (isDialogCancel(error)) return
    ElMessage.error('无法完成弃用确认')
    return
  }
  if (!datasetCapabilities.value.catalogEnabled || !canManageDataset(dataset)) {
    ElMessage.warning('数据集治理能力或当前权限已变更，请刷新后重试')
    return
  }

  setBooleanFlag(deprecating, version.id, true)
  try {
    const updated = normalizeDatasetVersions([await deprecateDatasetVersion(dataset.id, version.id)])[0]
    if (!updated || updated.id !== version.id || updated.state !== 'DEPRECATED') throw new Error('invalid deprecated version response')
    versionsByDataset.value = {
      ...versionsByDataset.value,
      [dataset.id]: versionsFor(dataset.id).map((item) => item.id === updated?.id ? updated : item),
    }
    ElMessage.success(`版本 ${version.version} 已弃用`)
  } catch (error) {
    ElMessage.error(safeErrorMessage(error, '弃用数据集版本失败'))
  } finally {
    setBooleanFlag(deprecating, version.id, false)
  }
}

const previewGarbageCollection = async () => {
  if (!effectiveSuperAdmin.value || !datasetCapabilities.value.publisherEnabled) return
  try {
    await ElMessageBox.confirm(
      '将计算可回收的已弃用版本。本次操作只生成预览，不会删除任何内容。',
      '回收预览',
      { confirmButtonText: '生成预览', cancelButtonText: '取消', type: 'info' },
    )
  } catch (error) {
    if (isDialogCancel(error)) return
    ElMessage.error('无法完成回收预览确认')
    return
  }
  if (!effectiveSuperAdmin.value || !datasetCapabilities.value.publisherEnabled) {
    ElMessage.warning('回收预览能力或当前权限已变更，请刷新后重试')
    return
  }

  gcLoading.value = true
  try {
    const response = await previewDatasetGarbageCollection()
    const candidates = normalizeDatasetVersions(response?.candidates)
    gcPreview.value = { count: candidates.length, candidates }
    gcDialogVisible.value = true
  } catch (error) {
    ElMessage.error(safeErrorMessage(error, '生成回收预览失败'))
  } finally {
    gcLoading.value = false
  }
}

watch(
  () => [datasetCapabilities.value.catalogEnabled, canManageCatalog.value],
  ([catalogEnabled, manageable]) => {
    ++loadGeneration
    if (!catalogEnabled || !manageable) {
      clearCatalogState()
      loading.value = false
      return
    }
    loadDatasets()
  },
  { immediate: true },
)

watch(effectiveSuperAdmin, (enabled) => {
  if (!enabled && createForm.value.visibility === 'PUBLIC') syncSourceSpace()
})
</script>

<style scoped>
.field-help {
  margin-top: 0.25rem;
  width: 100%;
  font-size: 0.6875rem;
  line-height: 1.25rem;
  color: rgb(100 116 139);
}
</style>
