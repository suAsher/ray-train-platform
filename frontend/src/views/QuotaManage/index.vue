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
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { apiDelete, apiGet, apiPost } from '../../api/client'

const tenants = ref([])
const users = ref([])
const showAddTenantModal = ref(false)
const showAddUserModal = ref(false)
const creatingUser = ref(false)
const newUser = ref({ username: '', password: '', role: 'Engineer' })

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
})
</script>
