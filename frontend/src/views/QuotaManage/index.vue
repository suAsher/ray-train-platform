<template>
  <div class="space-y-6">
    <div class="panel flex flex-wrap items-center justify-between gap-4 p-6">
      <div>
        <h3 class="flex items-center gap-2 text-lg font-bold text-white">
          <el-icon class="text-purple-400"><Lock /></el-icon> 管理员控制台
        </h3>
        <p class="mt-1 text-xs text-slate-400">{{ quotaCopy.pageSummary }}</p>
      </div>
      <div class="flex items-center gap-3">
        <el-tag :type="isSuperAdmin ? 'danger' : 'warning'" effect="dark" size="small">
          {{ isSuperAdmin ? '超级管理员 (SuperAdmin)' : '团队管理员 (TenantAdmin)' }}
        </el-tag>
        <el-button size="small" :loading="loading" icon="Refresh" @click="loadAll">刷新</el-button>
      </div>
    </div>

    <el-tabs v-model="activeTab" type="border-card" class="!rounded-2xl !border-slate-800/80 !bg-[#131826] shadow-xl">
      <el-tab-pane label="租户与配额" name="tenants">
        <div class="p-4">
          <TenantPanel
            :tenants="tenants"
            :is-super-admin="isSuperAdmin"
            :limits="limits"
            @create-tenant="showAddTenantModal = true"
            @changed="loadTenants"
          />
        </div>
      </el-tab-pane>

      <el-tab-pane label="用户与权限" name="users">
        <div class="p-4">
          <UserPanel
            :users="users"
            :is-super-admin="isSuperAdmin"
            :storage-quota-enabled="storageQuotaEnabled"
            :preparing="preparingObjectSet"
            :can-manage="canManageUser"
            @create-user="showAddUserModal = true"
            @reset-password="openResetPassword"
            @set-state="changeUserState"
            @decommission="decommissionUser"
            @edit-storage="openStorageQuota"
            @edit-roles="openRoles"
            @prepare-object-set="prepareObjectSet"
          />
        </div>
      </el-tab-pane>

      <el-tab-pane label="镜像与凭据" name="catalog">
        <div class="p-4">
          <CatalogPanel
            :images="catalogImages"
            :credentials="gitCredentials"
            :is-super-admin="isSuperAdmin"
            :current-tenant-id="currentTenantId"
            @create-image="showAddImageModal = true"
            @edit-scope="changeImageScope"
            @remove-image="removeImage"
            @create-credential="showAddCredentialModal = true"
            @remove-credential="removeCredential"
            @test-credential="testCredential"
          />
        </div>
      </el-tab-pane>

      <el-tab-pane label="数据与存储" name="storage">
        <div class="p-4">
          <StoragePanel :is-super-admin="isSuperAdmin" />
        </div>
      </el-tab-pane>

      <el-tab-pane v-if="datasetCapabilities.catalogEnabled" label="数据集治理" name="datasets">
        <div class="p-4">
          <DatasetGovernancePanel
            :capabilities="datasetCapabilities"
            :is-super-admin="isSuperAdmin"
          />
        </div>
      </el-tab-pane>

      <el-tab-pane label="队列与运行中" name="queue">
        <div class="p-4">
          <QueuePanel
            :jobs="activeJobs"
            :allocations="gpuAllocations"
            :allocation-available="gpuAllocationsLoaded"
            :allocation-error="gpuAllocationError"
            :cluster-g-p-us="clusterGPUs"
            :physical-allocated-g-p-us="physicalAllocatedGPUs"
            :current-tenant-id="currentTenantId"
            :is-super-admin="isSuperAdmin"
            @cancel-job="cancelJob"
          />
        </div>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="showAddTenantModal" title="新建租户 / 团队" width="440px">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="租户 ID"><el-input v-model="newTenant.id" placeholder="小写字母、数字或短横线，例如 team-a" /></el-form-item>
        <el-form-item label="显示名称"><el-input v-model="newTenant.name" placeholder="例如 感知算法组" /></el-form-item>
        <el-form-item label="GPU 配额（卡）">
          <el-input-number v-model="newTenant.gpuQuota" :min="0" :max="4096" class="w-full" />
        </el-form-item>
        <p class="text-[11px] text-slate-500">将同时创建 Kubernetes namespace 与 Kueue 队列。</p>
      </el-form>
      <template #footer>
        <el-button @click="showAddTenantModal = false">取消</el-button>
        <el-button type="primary" :loading="creatingTenant" @click="submitTenant">创建</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showAddImageModal" title="登记镜像" width="480px">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="名称"><el-input v-model="newImage.name" placeholder="例如 BEVFusion CUDA 12.1" /></el-form-item>
        <el-form-item label="用途">
          <el-select v-model="newImage.kind" class="w-full">
            <el-option label="训练任务" value="training" />
            <el-option label="交互式调试" value="workspace" />
          </el-select>
        </el-form-item>
        <el-form-item label="Ray 版本">
          <el-select v-model="newImage.rayVersion" class="w-full">
            <el-option label="2.35.0（兼容版本）" value="2.35.0" />
            <el-option label="2.56.1（生产版本）" value="2.56.1" />
            <el-option label="2.58.0（前沿版本）" value="2.58.0" />
          </el-select>
        </el-form-item>
        <el-form-item label="支持的训练引擎" required>
          <el-checkbox-group v-model="newImage.supportedEngines">
            <el-checkbox label="ray-ddp">ray-ddp</el-checkbox>
            <el-checkbox label="ray-train" :disabled="newImage.rayVersion === '2.35.0'">ray-train</el-checkbox>
          </el-checkbox-group>
          <p class="mt-1 w-full text-[11px] text-slate-500">至少选择一个训练引擎；Ray 2.35.0 仅支持 ray-ddp。</p>
        </el-form-item>
        <el-form-item label="镜像（显式 tag 或 @sha256 digest）">
          <el-input v-model="newImage.reference" placeholder="registry/project/image:tag 或 registry/project/image@sha256:..." />
          <p class="mt-1 text-[11px] text-slate-500">tag 便于日常迭代，每次启动都会重新拉取；正式基线推荐使用不可变 digest。</p>
        </el-form-item>
        <el-form-item label="框架标注"><el-input v-model="newImage.framework" placeholder="可选，例如 PyTorch / BEVFusion" /></el-form-item>
        <div class="flex gap-6">
          <el-checkbox v-model="newImage.isDefault">设为该用途的默认镜像</el-checkbox>
          <el-checkbox v-model="newImage.shared" :disabled="!isSuperAdmin">全平台共享</el-checkbox>
        </div>
      </el-form>
      <template #footer>
        <el-button @click="showAddImageModal = false">取消</el-button>
        <el-button type="primary" :loading="creatingImage" @click="submitImage">登记</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showAddCredentialModal" title="添加团队私有仓库凭据" width="440px">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="名称"><el-input v-model="newCredential.name" placeholder="例如 内网 GitLab" /></el-form-item>
        <el-form-item label="Git 主机"><el-input v-model="newCredential.host" placeholder="gitlab.qomolo.com（只填主机名）" /></el-form-item>
        <el-form-item label="用户名"><el-input v-model="newCredential.username" placeholder="留空则使用 git" /></el-form-item>
        <el-form-item label="访问令牌 / 密码">
          <el-input v-model="newCredential.token" type="password" show-password placeholder="Personal Access Token" />
        </el-form-item>
        <p class="text-[11px] text-slate-500">团队成员提交该 Git 主机的任务时作为兜底凭据；令牌只写入 Kubernetes Secret。</p>
      </el-form>
      <template #footer>
        <el-button @click="showAddCredentialModal = false">取消</el-button>
        <el-button type="primary" :loading="creatingCredential" @click="submitCredential">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showAddUserModal" title="添加平台账号" width="440px">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="用户名"><el-input v-model="newUser.username" placeholder="例如 zhangsan" autocomplete="off" /></el-form-item>
        <el-form-item label="初始密码">
          <el-input v-model="newUser.password" type="password" show-password placeholder="至少 8 位" autocomplete="new-password" />
        </el-form-item>
        <el-form-item label="角色">
          <el-select v-model="newUser.role" class="w-full">
            <el-option label="Engineer（提交与查看自己的任务）" value="Engineer" />
            <el-option v-if="isSuperAdmin" label="TenantAdmin（管理本团队成员与团队共享目录）" value="TenantAdmin" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="isSuperAdmin" label="所属租户">
          <el-select v-model="newUser.tenantId" class="w-full" placeholder="选择已创建的租户">
            <el-option v-for="tenant in tenants" :key="tenant.id" :label="`${tenant.name || tenant.id}（${tenant.id}）`" :value="tenant.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="storageQuotaEnabled" label="个人 TOS 硬配额">
          <el-input-number v-model="newUser.storageQuotaGiB" :min="1" :max="storageQuotaMaxGiB" :step="10" class="w-full" />
          <p class="mt-1 text-[11px] text-slate-500">单位 GiB，当前平台允许 1–{{ storageQuotaMaxGiB }} GiB。</p>
        </el-form-item>
        <p class="text-[11px] text-slate-500">创建时会自动初始化个人工作区、文件、训练结果与快照目录。</p>
      </el-form>
      <template #footer>
        <el-button @click="showAddUserModal = false">取消</el-button>
        <el-button type="primary" :loading="creatingUser" @click="createUser">创建</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showRolesModal" title="修改账号角色" width="460px" @closed="selectedRoleUser = null">
      <p class="mb-4 text-sm leading-6 text-slate-400">
        为 <span class="font-mono text-slate-200">{{ selectedRoleUser?.username }}</span>
        （租户 <span class="font-mono text-slate-200">{{ selectedRoleUser?.tenant_id }}</span>）设置角色。
        保存后立即生效，该用户已登录的会话会退出并需要重新登录。
      </p>
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="角色">
          <el-select v-model="selectedRole" class="w-full">
            <el-option label="Engineer — 提交与查看自己的任务，只写个人空间" value="Engineer" />
            <el-option label="TenantAdmin — 额外可管理本团队成员，并对团队共享目录有写权限" value="TenantAdmin" />
          </el-select>
        </el-form-item>
      </el-form>
      <el-alert v-if="selectedRole === 'TenantAdmin'" type="info" :closable="false" show-icon>
        <template #title>TenantAdmin 可以向「团队共享数据」发布文件</template>
        该目录对本团队所有成员只读可见，训练任务可直接选它作为输入。
      </el-alert>
      <template #footer>
        <el-button @click="showRolesModal = false">取消</el-button>
        <el-button type="primary" :loading="savingRoles" @click="submitRoles">保存角色</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showStorageQuotaModal" title="配置个人 TOS 硬配额" width="420px" @closed="resetStorageQuotaForm">
      <p class="mb-4 text-sm text-slate-400">
        为 <span class="font-mono text-slate-200">{{ selectedStorageUser?.username }}</span> 设置可写入的最大容量。
        达到上限后 TOS 会拒绝新写入；不会删除已有文件。
      </p>
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="容量（GiB）">
          <el-input-number v-model="storageQuotaGiB" :min="1" :max="storageQuotaMaxGiB" :step="10" class="w-full" @keyup.enter="submitStorageQuota" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showStorageQuotaModal = false">取消</el-button>
        <el-button type="primary" :loading="savingStorageQuota" @click="submitStorageQuota">保存配额</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="showResetPasswordModal" title="重置本地账号密码" width="420px" @closed="resetPasswordForm">
      <p class="mb-4 text-sm text-slate-400">
        为 <span class="font-mono text-slate-200">{{ selectedUser?.username }}</span> 设置新密码。保存后该用户所有已登录设备会退出。
      </p>
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="新密码">
          <el-input v-model="resetPassword" type="password" show-password autocomplete="new-password" placeholder="至少 8 位" @keyup.enter="submitPasswordReset" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showResetPasswordModal = false">取消</el-button>
        <el-button type="primary" :loading="resettingPassword" @click="submitPasswordReset">重置并退出旧会话</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import { apiDelete, apiGet, apiPost } from '../../api/client'
import { createGitCredential, createImage, createTenant, deleteGitCredential, deleteImage, fetchGitCredentials, fetchImages, testGitCredential, updateImageScope } from '../../api/catalog'
import { fetchPlatformLimits } from '../../api/platform'
import { adminQuotaModel, defaultPlatformLimits } from '../../platformLimits'
import { normalizeDatasetCapabilities } from '../../datasetCatalog.js'
import { roles, session } from '../../stores/session'
import { formatStorageQuota, storageQuotaGiBFromQuantity, storageQuotaGiBToBytes } from '../../storageQuota'
import TenantPanel from '../../components/admin/TenantPanel.vue'
import UserPanel from '../../components/admin/UserPanel.vue'
import CatalogPanel from '../../components/admin/CatalogPanel.vue'
import StoragePanel from '../../components/admin/StoragePanel.vue'
import QueuePanel from '../../components/admin/QueuePanel.vue'
import DatasetGovernancePanel from '../../components/admin/DatasetGovernancePanel.vue'
import { queueJobAction } from '../../components/admin/queuePanelActions.js'
import { normalizeGPUAllocations } from '../../gpuAllocations'
import { buildCreateImageRequest, defaultImageCompatibilityState, reconcileImageCompatibility } from '../../imageCompatibility'

const activeTab = ref('tenants')
const loading = ref(false)
const tenants = ref([])
const users = ref([])
const activeJobs = ref([])
const gpuAllocations = ref([])
const gpuAllocationsLoaded = ref(false)
const gpuAllocationError = ref('')
const physicalAllocatedGPUs = ref(0)
const catalogImages = ref([])
const gitCredentials = ref([])
const limits = ref(defaultPlatformLimits)

const isSuperAdmin = computed(() => roles.value.includes('SuperAdmin'))
const currentTenantId = computed(() => session.value?.tenantId || '')
const quotaCopy = computed(() => adminQuotaModel({
  isSuperAdmin: isSuperAdmin.value,
  limits: limits.value,
  tenants: tenants.value,
}))
const clusterGPUs = computed(() => quotaCopy.value.capacityGPUs)
const datasetCapabilities = computed(() => normalizeDatasetCapabilities(limits.value.datasets))

const storageQuotaEnabled = Boolean(window.__RAY_PLATFORM_CONFIG__?.personalStorageQuotaEnabled)
const runtimeStorageQuotaDefault = storageQuotaGiBFromQuantity(window.__RAY_PLATFORM_CONFIG__?.personalStorageDefaultQuota) || 100
const runtimeStorageQuotaMax = storageQuotaGiBFromQuantity(window.__RAY_PLATFORM_CONFIG__?.personalStorageMaxQuota) || 102400
const storageQuotaMaxGiB = Math.max(1, runtimeStorageQuotaMax)

const showAddTenantModal = ref(false)
const showAddUserModal = ref(false)
const showAddImageModal = ref(false)
const showAddCredentialModal = ref(false)
const showResetPasswordModal = ref(false)
const showStorageQuotaModal = ref(false)
const creatingTenant = ref(false)
const creatingUser = ref(false)
const creatingImage = ref(false)
const creatingCredential = ref(false)
const resettingPassword = ref(false)
const savingStorageQuota = ref(false)
const preparingObjectSet = ref(false)
const selectedUser = ref(null)
const selectedStorageUser = ref(null)
const resetPassword = ref('')
const storageQuotaGiB = ref(Math.min(runtimeStorageQuotaDefault, storageQuotaMaxGiB))

const newTenant = ref({ id: '', name: '', gpuQuota: 8 })
const newUser = ref({ username: '', password: '', role: 'Engineer', tenantId: '', storageQuotaGiB: Math.min(runtimeStorageQuotaDefault, storageQuotaMaxGiB) })
const emptyImageForm = () => ({
  name: '',
  kind: 'training',
  reference: '',
  framework: '',
  isDefault: false,
  ...defaultImageCompatibilityState(),
  shared: false,
})
const newImage = ref(emptyImageForm())
const newCredential = ref({ name: '', host: '', username: '', token: '', scope: 'team' })

watch(() => newImage.value.rayVersion, (rayVersion) => {
  newImage.value = reconcileImageCompatibility(newImage.value, rayVersion)
})

watch(() => datasetCapabilities.value.catalogEnabled, (enabled) => {
  if (!enabled && activeTab.value === 'datasets') activeTab.value = 'tenants'
})

const loadTenants = async () => {
  try {
    tenants.value = (await apiGet('/api/v1/tenants')) || []
  } catch (error) {
    tenants.value = []
    ElMessage.error(error.message || '无法读取租户配额')
  }
}

const loadUsers = async () => {
  try {
    const items = (await apiGet('/api/v1/local-users')) || []
    users.value = items.map((item) => ({ ...item, role: item.roles?.[0] || 'Engineer', tenant_id: item.tenantId }))
  } catch (error) {
    users.value = []
    ElMessage.error(error.message || '无法读取平台用户')
  }
}

// Both queued and running jobs matter to an administrator: the running ones are
// what actually hold the GPUs a queued job is waiting for.
const loadActiveJobs = async () => {
  const states = ['SUBMITTED', 'VALIDATING', 'QUEUED', 'ADMITTED', 'PROVISIONING', 'RUNNING', 'RECOVERING']
  const pages = await Promise.allSettled(states.map((state) => apiGet(`/api/v1/jobs?status=${state}`)))
  const rowsByID = new Map()
  for (const page of pages) {
    if (page.status !== 'fulfilled') continue
    for (const job of page.value?.items || []) {
      const resources = job.spec?.resources || {}
      rowsByID.set(job.id, {
        id: job.id,
        name: job.spec?.name || job.id,
        tenantId: job.tenantId,
        state: job.observedState,
        gpus: (resources.workerReplicas || 0) * (resources.gpusPerWorker || 0),
        createdAt: job.createdAt ? new Date(job.createdAt).toLocaleString('zh-CN', { hour12: false }) : '',
      })
    }
  }
  activeJobs.value = [...rowsByID.values()]
}

const loadClusterTopology = async () => {
  try {
    const topology = await apiGet('/api/v1/cluster/topology')
    physicalAllocatedGPUs.value = Number(topology?.usedGpus || 0)
  } catch {
    physicalAllocatedGPUs.value = 0
  }
}

const loadGPUAllocations = async () => {
  try {
    gpuAllocations.value = normalizeGPUAllocations(await apiGet('/api/v1/gpu-allocations'))
    gpuAllocationsLoaded.value = true
    gpuAllocationError.value = ''
  } catch (error) {
    gpuAllocationError.value = error.message || '无法读取 GPU 占用明细'
    ElMessage.error(gpuAllocationError.value)
  }
}

const loadCatalog = async () => {
  const [images, credentials] = await Promise.allSettled([fetchImages(), fetchGitCredentials()])
  catalogImages.value = images.status === 'fulfilled' ? images.value || [] : []
  gitCredentials.value = credentials.status === 'fulfilled' ? credentials.value || [] : []
}

const loadLimits = async () => {
  try {
    limits.value = { ...defaultPlatformLimits, ...(await fetchPlatformLimits()) }
  } catch {
    limits.value = defaultPlatformLimits
  }
}

const loadAll = async () => {
  loading.value = true
  await Promise.all([loadTenants(), loadUsers(), loadActiveJobs(), loadGPUAllocations(), loadClusterTopology(), loadCatalog(), loadLimits()])
  loading.value = false
}

const submitTenant = async () => {
  if (!newTenant.value.id) {
    ElMessage.warning('请填写租户 ID')
    return
  }
  creatingTenant.value = true
  try {
    await createTenant(newTenant.value)
    ElMessage.success(`租户 ${newTenant.value.id} 已创建（含 namespace 与队列）`)
    showAddTenantModal.value = false
    newTenant.value = { id: '', name: '', gpuQuota: 8 }
    await loadTenants()
  } catch (error) {
    ElMessage.error(error.message || '创建租户失败')
  } finally {
    creatingTenant.value = false
  }
}

const submitImage = async () => {
  if (!newImage.value.name || !newImage.value.reference) {
    ElMessage.warning('请填写名称与镜像地址')
    return
  }
  if (newImage.value.supportedEngines.length === 0) {
    ElMessage.warning('请至少选择一个训练引擎')
    return
  }
  creatingImage.value = true
  try {
    await createImage(buildCreateImageRequest(newImage.value))
    ElMessage.success('镜像已登记')
    showAddImageModal.value = false
    newImage.value = emptyImageForm()
    await loadCatalog()
  } catch (error) {
    ElMessage.error(error.message || '登记镜像失败')
  } finally {
    creatingImage.value = false
  }
}

const removeImage = async (id) => {
  try {
    await deleteImage(id)
    await loadCatalog()
  } catch (error) {
    ElMessage.error(error.message || '删除镜像失败')
  }
}

const changeImageScope = async (image) => {
  if (!isSuperAdmin.value) return
  const shared = Boolean(image.tenantId)
  const target = shared ? '全平台' : '本团队'
  try {
    await ElMessageBox.confirm(`确定将镜像 ${image.name} 的可用范围改为${target}吗？`, '修改镜像范围', {
      type: 'warning', confirmButtonText: `改为${target}`, cancelButtonText: '取消',
    })
    await updateImageScope(image.id, shared, shared ? '' : currentTenantId.value)
    ElMessage.success(`镜像已改为${target}可用`)
    await loadCatalog()
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error.message || '修改镜像范围失败')
  }
}

const submitCredential = async () => {
  if (!newCredential.value.host || !newCredential.value.token) {
    ElMessage.warning('请填写 Git 主机与访问令牌')
    return
  }
  creatingCredential.value = true
  try {
    await createGitCredential(newCredential.value)
    ElMessage.success('凭证已保存到 Kubernetes Secret')
    showAddCredentialModal.value = false
    newCredential.value = { name: '', host: '', username: '', token: '', scope: 'team' }
    await loadCatalog()
  } catch (error) {
    ElMessage.error(error.message || '保存凭证失败')
  } finally {
    creatingCredential.value = false
  }
}

const removeCredential = async (id) => {
  try {
    await deleteGitCredential(id)
    await loadCatalog()
  } catch (error) {
    ElMessage.error(error.message || '删除凭证失败')
  }
}

const testCredential = async (credential) => {
  try {
    const { value } = await ElMessageBox.prompt(
      `输入 ${credential.host} 上一个团队可读取的 HTTPS 仓库地址。平台只会访问这个已批准的主机，不会显示令牌。`,
      '测试团队 Git 凭据',
      { inputPlaceholder: `https://${credential.host}/group/repository.git`, inputPattern: /^https:\/\//, inputErrorMessage: '请输入 HTTPS 仓库地址', confirmButtonText: '开始测试', cancelButtonText: '取消' },
    )
    const result = await testGitCredential(credential.id, value)
    if (result.authenticated) ElMessage.success(result.message || '仓库连接与权限验证成功')
    else ElMessage.warning(result.message || 'Git 主机可达，但凭据没有该仓库权限')
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error.message || 'Git 凭据测试失败')
  }
}

const createUser = async () => {
  if (!newUser.value.username || !newUser.value.password) {
    ElMessage.warning('请填写用户名和初始密码')
    return
  }
  if (storageQuotaEnabled && (storageQuotaGiBToBytes(newUser.value.storageQuotaGiB) === null || newUser.value.storageQuotaGiB > storageQuotaMaxGiB)) {
    ElMessage.warning(`请输入 1 到 ${storageQuotaMaxGiB} 之间的整数 GiB 容量`)
    return
  }
  if (isSuperAdmin.value && !newUser.value.tenantId) {
    ElMessage.warning('请选择账号所属租户')
    return
  }
  creatingUser.value = true
  try {
    await apiPost('/api/v1/local-users', {
      username: newUser.value.username,
      password: newUser.value.password,
      roles: [newUser.value.role],
      ...(storageQuotaEnabled ? { storageQuotaGiB: newUser.value.storageQuotaGiB } : {}),
      ...(isSuperAdmin.value ? { tenantId: newUser.value.tenantId } : {}),
    })
    ElMessage.success(`账号 ${newUser.value.username} 与个人数据空间已创建`)
    showAddUserModal.value = false
    newUser.value = { username: '', password: '', role: 'Engineer', tenantId: '', storageQuotaGiB: Math.min(runtimeStorageQuotaDefault, storageQuotaMaxGiB) }
    await loadUsers()
  } catch (error) {
    ElMessage.error(error.message || '创建账号失败')
  } finally {
    creatingUser.value = false
  }
}

const canManageUser = (user) => {
  if (!user || user.id === session.value?.subject || user.roles?.includes('SuperAdmin')) return false
  if (isSuperAdmin.value) return true
  return user.tenant_id === session.value?.tenantId && user.roles?.length === 1 && user.roles[0] === 'Engineer'
}

const resetPasswordForm = () => {
  selectedUser.value = null
  resetPassword.value = ''
}

const openResetPassword = (user) => {
  selectedUser.value = user
  resetPassword.value = ''
  showResetPasswordModal.value = true
}

const submitPasswordReset = async () => {
  if (!selectedUser.value) return
  if (resetPassword.value.length < 8) {
    ElMessage.warning('新密码至少需要 8 位')
    return
  }
  resettingPassword.value = true
  try {
    await apiPost(`/api/v1/local-users/${selectedUser.value.id}/reset-password`, { newPassword: resetPassword.value })
    ElMessage.success('密码已重置，旧会话已失效')
    showResetPasswordModal.value = false
    await loadUsers()
  } catch (error) {
    ElMessage.error(error.message || '重置密码失败')
  } finally {
    resettingPassword.value = false
  }
}

const changeUserState = async (user, disabled) => {
  const action = disabled ? '停用' : '启用'
  try {
    await ElMessageBox.confirm(`确定要${action}账号 ${user.username} 吗？${disabled ? '该用户现有登录会话会立即失效。' : ''}`, `${action}账号`, {
      type: disabled ? 'warning' : 'info', confirmButtonText: `确认${action}`, cancelButtonText: '取消',
    })
    await apiPost(`/api/v1/local-users/${user.id}/${disabled ? 'disable' : 'enable'}`, {})
    ElMessage.success(`账号已${action}`)
    await loadUsers()
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error.message || `${action}账号失败`)
  }
}

const decommissionUser = async (user) => {
  try {
    await ElMessageBox.confirm(
      `将删除账号 ${user.username}。账号会立即停用并退出所有会话；个人 TOS 数据、训练记录和结果不会被删除。请先停止该用户的训练任务和调试环境。`,
      '删除用户',
      { type: 'warning', confirmButtonText: '确认删除账号', cancelButtonText: '取消' },
    )
    await apiDelete(`/api/v1/local-users/${user.id}`)
    ElMessage.success(`账号 ${user.username} 已删除，个人数据仍按平台保留策略保存`)
    await loadUsers()
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error.message || '删除账号失败')
  }
}

const resetStorageQuotaForm = () => {
  selectedStorageUser.value = null
  storageQuotaGiB.value = Math.min(runtimeStorageQuotaDefault, storageQuotaMaxGiB)
}

const openStorageQuota = (user) => {
  selectedStorageUser.value = user
  storageQuotaGiB.value = user.storageQuota?.bytes
    ? Math.max(1, Math.round(user.storageQuota.bytes / (1024 * 1024 * 1024)))
    : Math.min(runtimeStorageQuotaDefault, storageQuotaMaxGiB)
  showStorageQuotaModal.value = true
}

const submitStorageQuota = async () => {
  if (!selectedStorageUser.value || storageQuotaGiBToBytes(storageQuotaGiB.value) === null || storageQuotaGiB.value > storageQuotaMaxGiB) {
    ElMessage.warning(`请输入 1 到 ${storageQuotaMaxGiB} 之间的整数 GiB 容量`)
    return
  }
  savingStorageQuota.value = true
  try {
    const quota = await apiPost(`/api/v1/local-users/${selectedStorageUser.value.id}/storage-quota`, { storageQuotaGiB: storageQuotaGiB.value })
    ElMessage.success(`已将个人 TOS 硬配额调整为 ${formatStorageQuota(quota.bytes)}`)
    showStorageQuotaModal.value = false
    await loadUsers()
  } catch (error) {
    ElMessage.error(error.message || '保存存储配额失败')
  } finally {
    savingStorageQuota.value = false
  }
}

const showRolesModal = ref(false)
const selectedRoleUser = ref(null)
const selectedRole = ref('Engineer')
const savingRoles = ref(false)

const openRoles = (user) => {
  selectedRoleUser.value = user
  selectedRole.value = user.roles?.includes('TenantAdmin') ? 'TenantAdmin' : 'Engineer'
  showRolesModal.value = true
}

const submitRoles = async () => {
  if (!selectedRoleUser.value) return
  savingRoles.value = true
  try {
    await apiPost(`/api/v1/local-users/${selectedRoleUser.value.id}/roles`, { roles: [selectedRole.value] })
    ElMessage.success(`已将 ${selectedRoleUser.value.username} 的角色设为 ${selectedRole.value}`)
    showRolesModal.value = false
    await loadUsers()
  } catch (error) {
    ElMessage.error(error.message || '修改角色失败')
  } finally {
    savingRoles.value = false
  }
}

const prepareObjectSet = async () => {
  try {
    await ElMessageBox.confirm(
      '这会把当前 TOS Bucket 的 ObjectSet 前缀层级固定为平台个人目录的五级结构。该层级在已有 ObjectSet 后不能修改；不会删除或移动已有对象。确认继续吗？',
      '初始化 TOS 目录配额',
      { type: 'warning', confirmButtonText: '确认初始化', cancelButtonText: '取消' },
    )
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error.message || '无法确认初始化操作')
    return
  }
  preparingObjectSet.value = true
  try {
    await apiPost('/api/v1/storage-governance/objectset/prepare', {})
    ElMessage.success('ObjectSet 目录治理已初始化。请等待约一分钟后，为现有用户配置个人 TOS 容量。')
  } catch (error) {
    ElMessage.error(error.message || '初始化 TOS 目录配额失败')
  } finally {
    preparingObjectSet.value = false
  }
}

const cancelJob = async (job) => {
  const action = queueJobAction(job, currentTenantId.value, isSuperAdmin.value)
  if (!action) {
    ElMessage.warning('没有权限操作该团队的任务')
    return
  }
  const queued = action.kind === 'cancel-queue'
  try {
    await ElMessageBox.confirm(
      queued
        ? `将取消 ${job.tenantId} 的任务 ${job.name} 排队；任务不会进入运行。`
        : `将停止 ${job.tenantId} 的任务 ${job.name}（占用 ${job.gpus} 卡）。运行中的训练会立即中断。`,
      action.label,
      { type: 'warning', confirmButtonText: queued ? '确认取消排队' : '确认停止', cancelButtonText: '返回' },
    )
    await apiDelete(`/api/v1/jobs/${job.id}`)
    ElMessage.success(queued ? '已提交取消排队请求' : '已提交停止请求')
    await Promise.all([loadActiveJobs(), loadGPUAllocations(), loadClusterTopology(), loadTenants()])
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error.message || (queued ? '取消排队失败' : '停止任务失败'))
  }
}

onMounted(loadAll)
</script>
