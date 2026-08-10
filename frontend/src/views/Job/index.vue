<template>
  <div class="space-y-5">
    <!-- At-a-glance counters for the jobs the user actually cares about -->
    <div class="grid grid-cols-4 gap-4">
      <div v-for="card in summaryCards" :key="card.label" class="bg-[#131826] p-4 rounded-2xl border border-slate-800/80 shadow-lg">
        <p class="text-xs text-slate-400">{{ card.label }}</p>
        <p class="text-2xl font-bold mt-1" :class="card.tone">{{ card.value }}</p>
      </div>
    </div>

    <!-- Filter Header -->
    <div class="flex justify-between items-center bg-[#131826] p-5 rounded-2xl border border-slate-800/80 shadow-xl">
      <div class="flex items-center gap-4">
        <el-radio-group v-model="scope" size="default">
          <el-radio-button label="mine">我提交的</el-radio-button>
          <el-radio-button label="team">全team</el-radio-button>
        </el-radio-group>

        <el-input 
          v-model="searchKeyword" 
          placeholder="搜索任务名称 / ID / 关键词..." 
          style="width: 320px" 
          clearable 
          prefix-icon="Search"
        />

        <el-select v-model="statusFilter" placeholder="状态筛选" style="width: 150px" clearable>
          <el-option label="全部状态" value="" />
          <el-option label="排队中" value="QUEUED" />
          <el-option label="启动中" value="PROVISIONING" />
          <el-option label="运行中" value="RUNNING" />
          <el-option label="已成功" value="SUCCEEDED" />
          <el-option label="失败" value="FAILED" />
        </el-select>
      </div>

      <router-link to="/job/create">
        <el-button type="primary" icon="Plus" class="!rounded-xl">创建训练任务</el-button>
      </router-link>
    </div>

    <!-- Jobs Table -->
    <div class="bg-[#131826] rounded-2xl border border-slate-800/80 overflow-hidden shadow-2xl">
      <el-table :data="filteredJobs" style="width: 100%" class="!bg-transparent text-xs" v-loading="loading">
        <template #empty>
          <div class="py-12 space-y-3">
            <p class="text-slate-300 font-semibold">{{ emptyTitle }}</p>
            <p class="text-xs text-slate-500">{{ emptyHint }}</p>
            <router-link v-if="!hasAnyJob" to="/job/create">
              <el-button type="primary" class="!rounded-xl">提交第一个训练任务</el-button>
            </router-link>
          </div>
        </template>
        <el-table-column prop="name" label="任务名称 / ID" min-width="240">
          <template #default="scope">
            <div class="font-mono font-bold text-slate-100 hover:text-blue-400 cursor-pointer" @click="goToDetail(scope.row.id)">
              {{ scope.row.name }}
            </div>
            <div class="text-[11px] text-slate-500 font-sans truncate mt-0.5">{{ scope.row.entrypoint }}</div>
          </template>
        </el-table-column>

        <el-table-column prop="framework" label="训练框架" width="130">
          <template #default="scope">
            <el-tag size="small" effect="plain">{{ scope.row.framework || 'RayTrain' }}</el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="status" label="任务状态" width="140">
          <template #default="scope">
            <el-tag :type="getStatusType(scope.row.status)" size="small">
              {{ statusLabel(scope.row.status) }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="owner" label="提交人" width="130">
          <template #default="scope">
            <span class="text-slate-400">{{ scope.row.isMine ? '我' : shortOwner(scope.row.userId) }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="total_gpus" label="GPU 卡数" width="130">
          <template #default="scope">
            <span class="font-mono font-bold text-blue-400">{{ scope.row.total_gpus }} 卡</span>
            <span class="text-slate-500 text-[11px] ml-1">({{ scope.row.worker_replicas }}x{{ scope.row.gpus_per_worker }})</span>
          </template>
        </el-table-column>

        <el-table-column prop="created_at" label="创建时间" width="180">
          <template #default="scope">
            <span class="font-mono text-slate-400">{{ new Date(scope.row.created_at).toLocaleString() }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="160" fixed="right" align="right">
          <template #default="scope">
            <el-button type="primary" link size="small" @click="goToDetail(scope.row.id)">控制台</el-button>
            <el-button type="danger" link size="small" @click="deleteJob(scope.row.id)">停止</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiDelete, apiGet } from '../../api/client'
import { userId } from '../../stores/session'

const router = useRouter()
const scope = ref('mine')
const searchKeyword = ref('')
const statusFilter = ref('')
const loading = ref(false)
const jobs = ref([])
let refreshTimer

const normalizeJob = (job) => {
  const spec = job.spec || {}
  const resources = spec.resources || {}
  return {
    ...job,
    name: spec.name || job.name,
    status: job.observedState || 'UNKNOWN',
    isMine: job.userId === userId.value,
    entrypoint: [...(spec.entrypoint?.command || []), ...(spec.entrypoint?.args || [])].join(' '),
    worker_replicas: resources.workerReplicas || 0,
    gpus_per_worker: resources.gpusPerWorker || 0,
    total_gpus: (resources.workerReplicas || 0) * (resources.gpusPerWorker || 0),
    created_at: job.createdAt || job.created_at
  }
}

const fetchJobs = async () => {
  loading.value = true
  try {
    const query = new URLSearchParams()
    if (statusFilter.value) query.set('status', statusFilter.value)
    if (searchKeyword.value) query.set('keyword', searchKeyword.value)
    const page = await apiGet(`/api/v1/jobs${query.size ? `?${query}` : ''}`)
    jobs.value = (page.items || []).map(normalizeJob)
  } catch (error) {
    jobs.value = []
    ElMessage.error(error.message || '无法读取任务列表')
  } finally {
    loading.value = false
  }
}

// The API already scopes results to the caller's tenant; this narrows further
// to the jobs the signed-in user submitted.
const scopedJobs = computed(() => (scope.value === 'mine' ? jobs.value.filter((j) => j.isMine) : jobs.value))

const filteredJobs = computed(() => {
  const keyword = searchKeyword.value.trim().toLowerCase()
  return scopedJobs.value.filter((j) => {
    const matchKey = !keyword || j.name.toLowerCase().includes(keyword) || j.id.toLowerCase().includes(keyword)
    const matchStatus = !statusFilter.value || j.status === statusFilter.value
    return matchKey && matchStatus
  })
})

const hasAnyJob = computed(() => scopedJobs.value.length > 0)

const emptyTitle = computed(() => {
  if (loading.value) return '正在读取任务…'
  if (!hasAnyJob.value) return scope.value === 'mine' ? '你还没有提交过训练任务' : '团队还没有训练任务'
  return '没有符合当前筛选条件的任务'
})

const emptyHint = computed(() => {
  if (!hasAnyJob.value) return '选择镜像、代码来源和 GPU 规格即可提交，配额由平台自动校验。'
  return '试着清空关键词或状态筛选。'
})

const ACTIVE_STATES = ['QUEUED', 'PROVISIONING', 'RUNNING', 'SUBMITTED', 'ADMITTED']

const summaryCards = computed(() => {
  const items = scopedJobs.value
  const count = (predicate) => items.filter(predicate).length
  return [
    { label: '进行中', value: count((j) => ACTIVE_STATES.includes(j.status)), tone: 'text-blue-400' },
    { label: '已成功', value: count((j) => j.status === 'SUCCEEDED'), tone: 'text-emerald-400' },
    { label: '失败', value: count((j) => j.status === 'FAILED'), tone: 'text-red-400' },
    { label: '占用 GPU', value: items.filter((j) => ACTIVE_STATES.includes(j.status)).reduce((total, j) => total + j.total_gpus, 0), tone: 'text-slate-100' }
  ]
})

const STATUS_LABELS = {
  SUBMITTED: '已提交', VALIDATING: '校验中', QUEUED: '排队中', ADMITTED: '已准入',
  PROVISIONING: '启动中', RUNNING: '运行中', SUCCEEDED: '已成功', FAILED: '失败',
  CANCELING: '取消中', CANCELED: '已取消', TIMED_OUT: '已超时', DELETING: '清理中', UNKNOWN: '未知'
}
const statusLabel = (status) => STATUS_LABELS[status] || status
const shortOwner = (id) => (id ? `${id.slice(0, 8)}…` : '—')

const getStatusType = (status) => {
  switch (status) {
    case 'RUNNING': return 'primary'
    case 'SUCCEEDED': return 'success'
    case 'FAILED':
    case 'TIMED_OUT': return 'danger'
    case 'CANCELED': return 'info'
    default: return 'warning'
  }
}

const goToDetail = (id) => {
  router.push(`/job/detail/${id}`)
}

const deleteJob = async (id) => {
  try {
    await ElMessageBox.confirm('停止后任务将进入删除流程，是否继续？', '确认停止任务', { type: 'warning' })
    await apiDelete(`/api/v1/jobs/${id}`)
    ElMessage.success('已提交停止请求')
    await fetchJobs()
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') ElMessage.error(error.message || '停止任务失败')
  }
}

onMounted(() => {
  fetchJobs()
  refreshTimer = window.setInterval(fetchJobs, 5000)
})

onUnmounted(() => window.clearInterval(refreshTimer))
</script>
