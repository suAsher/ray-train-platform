<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex justify-between items-center bg-[#131826] p-6 rounded-2xl border border-slate-800/80 shadow-xl">
      <div>
        <h3 class="text-lg font-bold text-white flex items-center gap-2">
          <el-icon class="text-purple-400"><Lock /></el-icon> 多租户管理与 Kueue GPU 配额隔离 (RBAC & Quotas)
        </h3>
        <p class="text-xs text-slate-400 mt-1">支持基于 Kueue 的多租户 GPU 硬/软配额隔离、排队优先级调度与管理员 RBAC 权限管制。</p>
      </div>

      <div class="flex items-center gap-3">
        <el-tag type="danger" effect="dark" size="small">超级管理员权限 (SuperAdmin)</el-tag>
        <el-button type="primary" icon="Plus" class="!rounded-xl" @click="showAddTenantModal = true">新建租户/团队</el-button>
      </div>
    </div>

    <!-- Tenant Quota Cards Grid -->
    <div class="space-y-3">
      <h4 class="text-xs font-bold text-slate-300 uppercase tracking-wider">租户 / 项目组 GPU 配额使用情况 (24 卡总容量)</h4>

      <div class="grid grid-cols-3 gap-5">
        <div 
          v-for="tenant in tenants" 
          :key="tenant.id"
          class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 space-y-4 shadow-xl hover:border-slate-700 transition-all"
        >
          <div class="flex justify-between items-start">
            <div>
              <h5 class="text-sm font-bold text-white flex items-center gap-2">
                <el-icon class="text-blue-400"><UserFilled /></el-icon> {{ tenant.tenant_name }}
              </h5>
              <p class="text-[11px] font-mono text-slate-400 mt-0.5">Kueue 队列: {{ tenant.queue_name }}</p>
            </div>
            <el-tag size="small" :type="tenant.queued_jobs_count > 0 ? 'warning' : 'success'">
              {{ tenant.queued_jobs_count > 0 ? `${tenant.queued_jobs_count} 任务排队中` : '配额正常' }}
            </el-tag>
          </div>

          <!-- Quota Usage Bar -->
          <div class="space-y-1.5">
            <div class="flex justify-between text-xs font-mono">
              <span class="text-slate-400">4090 显卡配额:</span>
              <span class="font-bold text-blue-400">{{ tenant.gpu_quota_used }} / {{ tenant.gpu_quota_limit }} 卡</span>
            </div>
            <el-progress 
              :percentage="Math.round((tenant.gpu_quota_used / tenant.gpu_quota_limit) * 100)" 
              :status="tenant.gpu_quota_used === tenant.gpu_quota_limit ? 'warning' : 'success'"
              :show-text="false" 
            />
          </div>

          <div class="flex justify-between items-center text-xs font-mono text-slate-400 pt-2 border-t border-slate-800/60">
            <span>最高允许优先级: <span class="text-amber-400 font-bold uppercase">{{ tenant.max_priority }}</span></span>
            <el-button type="primary" link size="small" @click="editQuota(tenant)">修改配额</el-button>
          </div>
        </div>
      </div>
    </div>

    <!-- Queued Jobs Queue Management -->
    <div class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 space-y-4 shadow-xl">
      <div class="flex justify-between items-center">
        <div>
          <h4 class="text-sm font-bold text-white flex items-center gap-2">
            <el-icon class="text-amber-400"><Clock /></el-icon> 实时 Kueue 排队与抢占队列 (Queued & Pending Jobs)
          </h4>
          <p class="text-xs text-slate-400 mt-1">当集群显卡满载或租户超过配额时，新提交的任务自动进入组调度排队队列。</p>
        </div>
        <el-tag type="warning" size="small">Kueue Gang Scheduling 生效中</el-tag>
      </div>

      <el-table :data="queuedJobs" style="width: 100%" class="!bg-transparent text-xs">
        <el-table-column prop="name" label="排队任务名称" min-width="220">
          <template #default="scope">
            <span class="font-mono font-bold text-slate-200">{{ scope.row.name }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="tenant_name" label="所属租户" width="180" />
        
        <el-table-column prop="priority" label="优先级" width="120">
          <template #default="scope">
            <el-tag :type="scope.row.priority === 'HIGH' ? 'danger' : 'info'" size="small" effect="dark">
              {{ scope.row.priority }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="requested_gpus" label="申请 GPU 数量" width="140 font-mono text-blue-400 font-bold">
          <template #default="scope">
            {{ scope.row.requested_gpus }} 卡 4090
          </template>
        </el-table-column>

        <el-table-column prop="queued_time" label="已等待时间" width="140 font-mono text-amber-400" />

        <el-table-column label="管理员调度操作" width="180" align="right">
          <template #default="scope">
            <el-button type="danger" link size="small" @click="cancelJob(scope.row.id)">取消排队</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Users & RBAC Permissions Table -->
    <div class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 space-y-4 shadow-xl">
      <div class="flex justify-between items-center">
        <h4 class="text-sm font-bold text-white flex items-center gap-2">
          <el-icon class="text-blue-400"><Avatar /></el-icon> 平台用户与 RBAC 角色分配
        </h4>
        <el-button size="small" icon="User" @click="showAddUserModal = true">添加用户</el-button>
      </div>

      <el-table :data="users" style="width: 100%" class="!bg-transparent text-xs">
        <el-table-column prop="username" label="用户名" min-width="160 font-mono font-bold text-white" />
        <el-table-column prop="email" label="邮箱" min-width="200 font-mono text-slate-400" />
        <el-table-column prop="role" label="RBAC 角色" width="160">
          <template #default="scope">
            <el-tag :type="getRoleTag(scope.row.role)" size="small" effect="plain">
              {{ scope.row.role }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="tenant_id" label="所属租户组" width="180 font-mono" />
        <el-table-column prop="disabled" label="状态" width="100">
          <template #default="scope">
            <el-tag size="small" :type="scope.row.disabled ? 'info' : 'success'" effect="plain">
              {{ scope.row.disabled ? '已停用' : '正常' }}
            </el-tag>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Image catalogue: the environments users can pick for jobs and debugging -->
    <div class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 space-y-4 shadow-xl">
      <div class="flex justify-between items-center">
        <div>
          <h4 class="text-sm font-bold text-white flex items-center gap-2">
            <el-icon class="text-emerald-400"><Box /></el-icon> 镜像目录（训练 / 调试运行环境）
          </h4>
          <p class="text-[11px] text-slate-500 mt-1">用户在提交任务和启动调试环境时从这里选择，保证依赖环境一致且可复现。</p>
        </div>
        <el-button size="small" icon="Plus" @click="showAddImageModal = true">登记镜像</el-button>
      </div>

      <el-table :data="catalogImages" style="width: 100%" class="!bg-transparent text-xs" empty-text="尚未登记任何镜像">
        <el-table-column prop="name" label="名称" min-width="150" />
        <el-table-column prop="kind" label="用途" width="110">
          <template #default="scope">
            <el-tag size="small" :type="scope.row.kind === 'training' ? 'primary' : 'warning'" effect="plain">
              {{ scope.row.kind === 'training' ? '训练' : '调试' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="framework" label="框架" width="120" />
        <el-table-column prop="reference" label="镜像（digest）" min-width="260">
          <template #default="scope">
            <span class="font-mono text-[11px] text-slate-400 break-all">{{ scope.row.reference }}</span>
          </template>
        </el-table-column>
        <el-table-column label="范围" width="100">
          <template #default="scope">
            <el-tag size="small" effect="plain">{{ scope.row.tenantId ? '本团队' : '全平台' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90" align="right">
          <template #default="scope">
            <el-button type="danger" link size="small" @click="removeImage(scope.row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Private repository credentials -->
    <div class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 space-y-4 shadow-xl">
      <div class="flex justify-between items-center">
        <div>
          <h4 class="text-sm font-bold text-white flex items-center gap-2">
            <el-icon class="text-blue-400"><Key /></el-icon> 私有 Git 仓库凭证
          </h4>
          <p class="text-[11px] text-slate-500 mt-1">令牌保存在租户 namespace 的 Kubernetes Secret 中，数据库只记录引用；拉取私有仓库时自动注入。</p>
        </div>
        <el-button size="small" icon="Plus" @click="showAddCredentialModal = true">添加凭证</el-button>
      </div>

      <el-table :data="gitCredentials" style="width: 100%" class="!bg-transparent text-xs" empty-text="尚未配置私有仓库凭证">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column prop="host" label="Git 主机" min-width="180" />
        <el-table-column prop="username" label="用户名" width="140" />
        <el-table-column prop="secretName" label="Secret" min-width="180">
          <template #default="scope"><span class="font-mono text-[11px] text-slate-400">{{ scope.row.secretName }}</span></template>
        </el-table-column>
        <el-table-column label="操作" width="90" align="right">
          <template #default="scope">
            <el-button type="danger" link size="small" @click="removeCredential(scope.row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Create a tenant: database row, namespace and Kueue queue in one step -->
    <el-dialog v-model="showAddTenantModal" title="新建租户 / 团队" width="440px">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="租户 ID">
          <el-input v-model="newTenant.id" placeholder="小写字母、数字或短横线，例如 team-a" />
        </el-form-item>
        <el-form-item label="显示名称">
          <el-input v-model="newTenant.name" placeholder="例如 感知算法组" />
        </el-form-item>
        <el-form-item label="GPU 配额（卡）">
          <el-input-number v-model="newTenant.gpuQuota" :min="0" :max="512" class="w-full" />
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
        <el-form-item label="名称">
          <el-input v-model="newImage.name" placeholder="例如 PyTorch 2.4 + CUDA 12.1" />
        </el-form-item>
        <el-form-item label="用途">
          <el-select v-model="newImage.kind" class="w-full">
            <el-option label="训练任务" value="training" />
            <el-option label="交互式调试" value="workspace" />
          </el-select>
        </el-form-item>
        <el-form-item label="镜像（必须带 @sha256 digest）">
          <el-input v-model="newImage.reference" placeholder="registry/repo@sha256:..." />
        </el-form-item>
        <el-form-item label="框架标注">
          <el-input v-model="newImage.framework" placeholder="可选，例如 PyTorch / DeepSpeed" />
        </el-form-item>
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

    <el-dialog v-model="showAddCredentialModal" title="添加私有仓库凭证" width="440px">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="名称">
          <el-input v-model="newCredential.name" placeholder="例如 内网 GitLab" />
        </el-form-item>
        <el-form-item label="Git 主机">
          <el-input v-model="newCredential.host" placeholder="git.example.com（只填主机名）" />
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="newCredential.username" placeholder="留空则使用 git" />
        </el-form-item>
        <el-form-item label="访问令牌 / 密码">
          <el-input v-model="newCredential.token" type="password" show-password placeholder="Personal Access Token" />
        </el-form-item>
        <p class="text-[11px] text-slate-500">令牌只写入 Kubernetes Secret，不会保存到平台数据库，也不会在接口中返回。</p>
      </el-form>
      <template #footer>
        <el-button @click="showAddCredentialModal = false">取消</el-button>
        <el-button type="primary" :loading="creatingCredential" @click="submitCredential">保存</el-button>
      </template>
    </el-dialog>

    <!-- Create a local account so a colleague can sign in without an IdP -->
    <el-dialog v-model="showAddUserModal" title="添加平台账号" width="440px">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="用户名">
          <el-input v-model="newUser.username" placeholder="例如 zhangsan" autocomplete="off" />
        </el-form-item>
        <el-form-item label="初始密码">
          <el-input v-model="newUser.password" type="password" show-password placeholder="至少 8 位" autocomplete="new-password" />
        </el-form-item>
        <el-form-item label="角色">
          <el-select v-model="newUser.role" class="w-full">
            <el-option label="Engineer（提交与查看自己的任务）" value="Engineer" />
            <el-option label="TenantAdmin（管理本团队配额与成员）" value="TenantAdmin" />
          </el-select>
        </el-form-item>
        <p class="text-[11px] text-slate-500">账号会创建在当前团队下，创建后请让本人登录并修改密码。</p>
      </el-form>
      <template #footer>
        <el-button @click="showAddUserModal = false">取消</el-button>
        <el-button type="primary" :loading="creatingUser" @click="createUser">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { apiDelete, apiGet, apiPost } from '../../api/client'
import { createGitCredential, createImage, createTenant, deleteGitCredential, deleteImage, fetchGitCredentials, fetchImages } from '../../api/catalog'
import { roles } from '../../stores/session'

const tenants = ref([])
const users = ref([])
const showAddTenantModal = ref(false)
const showAddUserModal = ref(false)
const creatingUser = ref(false)
const newUser = ref({ username: '', password: '', role: 'Engineer' })
const catalogImages = ref([])
const gitCredentials = ref([])
const showAddImageModal = ref(false)
const showAddCredentialModal = ref(false)
const creatingTenant = ref(false)
const creatingImage = ref(false)
const creatingCredential = ref(false)
const isSuperAdmin = computed(() => roles.value.includes('SuperAdmin'))
const newTenant = ref({ id: '', name: '', gpuQuota: 24 })
const newImage = ref({ name: '', kind: 'training', reference: '', framework: '', isDefault: false, shared: false })
const newCredential = ref({ name: '', host: '', username: '', token: '' })

const loadCatalog = async () => {
  try {
    catalogImages.value = await fetchImages()
  } catch {
    catalogImages.value = []
  }
  try {
    gitCredentials.value = await fetchGitCredentials()
  } catch {
    gitCredentials.value = []
  }
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
    newTenant.value = { id: '', name: '', gpuQuota: 24 }
    await fetchTenants()
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
  creatingImage.value = true
  try {
    await createImage(newImage.value)
    ElMessage.success('镜像已登记')
    showAddImageModal.value = false
    newImage.value = { name: '', kind: 'training', reference: '', framework: '', isDefault: false, shared: false }
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
    newCredential.value = { name: '', host: '', username: '', token: '' }
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

const queuedJobs = ref([])

const fetchTenants = async () => {
  try {
    const items = await apiGet('/api/v1/tenants')
    tenants.value = (items || []).map(item => ({
      ...item,
      tenant_name: item.name,
      queue_name: item.queueName,
      gpu_quota_limit: item.gpuQuotaLimit,
      gpu_quota_used: item.gpuQuotaUsed,
      queued_jobs_count: item.queuedJobsCount,
      max_priority: item.maxPriority
    }))
  } catch (error) {
    tenants.value = []
    ElMessage.error(error.message || '无法读取租户配额')
  }
}

const fetchUsers = async () => {
  try {
    // Local accounts are the ones an administrator can actually manage here.
    const items = await apiGet('/api/v1/local-users')
    users.value = (items || []).map(item => ({ ...item, role: item.roles?.[0] || 'Engineer', tenant_id: item.tenantId }))
  } catch (error) {
    users.value = []
    ElMessage.error(error.message || '无法读取平台用户')
  }
}

const createUser = async () => {
  if (!newUser.value.username || !newUser.value.password) {
    ElMessage.warning('请填写用户名和初始密码')
    return
  }
  creatingUser.value = true
  try {
    await apiPost('/api/v1/local-users', {
      username: newUser.value.username,
      password: newUser.value.password,
      roles: [newUser.value.role]
    })
    ElMessage.success(`账号 ${newUser.value.username} 已创建`)
    showAddUserModal.value = false
    newUser.value = { username: '', password: '', role: 'Engineer' }
    await fetchUsers()
  } catch (error) {
    ElMessage.error(error.message || '创建账号失败')
  } finally {
    creatingUser.value = false
  }
}

const fetchQueuedJobs = async () => {
  try {
    const page = await apiGet('/api/v1/jobs?status=QUEUED')
    queuedJobs.value = (page.items || []).map(job => ({
      id: job.id,
      name: job.spec?.name || job.name,
      tenant_name: job.tenantId,
      priority: job.spec?.priority || 'normal',
      requested_gpus: (job.spec?.resources?.workerReplicas || 0) * (job.spec?.resources?.gpusPerWorker || 0),
      queued_time: job.createdAt ? new Date(job.createdAt).toLocaleString() : ''
    }))
  } catch (error) {
    queuedJobs.value = []
  }
}

const getRoleTag = (role) => {
  switch (role) {
    case 'SuperAdmin': return 'danger'
    case 'TenantAdmin': return 'warning'
    default: return 'info'
  }
}

const editQuota = (tenant) => {
  ElMessage.info('配额变更请通过 GitOps/Helm values 审批后发布')
}

const cancelJob = async (id) => {
  try {
    await apiDelete(`/api/v1/jobs/${id}`)
    ElMessage.success('已提交取消请求')
    await fetchQueuedJobs()
  } catch (error) {
    ElMessage.error(error.message || '取消任务失败')
  }
}

onMounted(() => {
  fetchTenants()
  fetchUsers()
  fetchQueuedJobs()
  loadCatalog()
})
</script>
