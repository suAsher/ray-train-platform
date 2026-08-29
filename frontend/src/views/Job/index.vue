<template>
  <div class="space-y-5">
    <!-- At-a-glance counters for the jobs the user actually cares about -->
    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summaryCards" :key="card.label" class="stat-tile panel-hover">
        <div class="flex items-center justify-between">
          <p class="stat-tile__label">{{ card.label }}</p>
          <span class="h-2 w-2 rounded-full" :class="card.dot" />
        </div>
        <p class="stat-tile__value" :class="card.tone">{{ card.value }}</p>
        <p class="text-[11px] text-slate-500">{{ card.hint }}</p>
      </div>
    </div>

    <!-- Filter Header -->
    <div class="panel flex flex-wrap items-center justify-between gap-4 p-5">
      <div class="flex flex-wrap items-center gap-3">
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
          <el-option label="恢复中" value="RECOVERING" />
          <el-option label="已成功" value="SUCCEEDED" />
          <el-option label="失败" value="FAILED" />
        </el-select>
      </div>

      <div class="flex items-center gap-3">
        <!-- Most submissions repeat a previous run with a small change, so
             re-running is a first-class entry rather than a blank form. -->
        <el-button v-if="lastJob" class="!rounded-xl" icon="RefreshRight" @click="rerun(lastJob)">复制上次任务</el-button>
        <router-link to="/job/create">
          <el-button type="primary" icon="Plus" class="!rounded-xl">创建训练任务</el-button>
        </router-link>
      </div>
    </div>

    <!-- Jobs Table -->
    <div class="panel overflow-hidden">
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
            <div class="cursor-pointer font-mono font-bold text-slate-100 hover:text-blue-400" @click="goToDetail(scope.row.id)">
              {{ scope.row.name }}
            </div>
            <div class="mt-0.5 flex items-center gap-1">
              <!-- The job id is what users paste into spk-rayjob commands. -->
              <code class="truncate font-mono text-[11px] text-slate-500">{{ scope.row.id }}</code>
              <el-button link size="small" icon="DocumentCopy" title="复制任务 ID" @click.stop="copyValue(scope.row.id)" />
            </div>
            <div class="mt-0.5 truncate font-sans text-[11px] text-slate-500">{{ scope.row.entrypoint }}</div>
          </template>
        </el-table-column>

        <el-table-column prop="framework" label="训练框架" width="130">
          <template #default="scope">
            <el-tag size="small" effect="plain">{{ scope.row.framework || 'RayTrain' }}</el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="submissionOrigin" label="提交方式" width="130">
          <template #default="scope">
            <el-tag size="small" effect="plain" :type="scope.row.submissionOrigin === 'ray-cli' ? 'warning' : 'info'">{{ originLabel(scope.row.submissionOrigin) }}</el-tag>
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
            <span class="text-slate-400">{{ ownerLabel(scope.row) }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="total_gpus" label="GPU 卡数" width="130">
          <template #default="scope">
            <span class="font-mono font-bold text-blue-400">{{ scope.row.total_gpus }} 卡</span>
            <span class="text-slate-500 text-[11px] ml-1">({{ scope.row.worker_replicas }}x{{ scope.row.gpus_per_worker }})</span>
          </template>
        </el-table-column>

        <el-table-column label="时间线" width="270">
          <template #default="scope">
            <div class="space-y-1 font-mono text-[11px]">
              <div class="text-slate-400">提交 {{ formatDateTime(scope.row.created_at) }}</div>
              <div class="text-slate-500">
                <span v-if="scope.row.timeline.isWaiting" class="text-amber-400">排队中 {{ scope.row.timeline.queuedLabel }}</span>
                <template v-else>结束 {{ finishedLabel(scope.row.timeline.finishedAt) }}</template>
              </div>
              <div class="text-slate-500">
                训练 <span :class="scope.row.timeline.isRunning ? 'text-blue-400' : ''">{{ scope.row.timeline.runningLabel }}</span>
                <span v-if="scope.row.timeline.queuedSeconds" class="ml-2 text-slate-600">排队 {{ scope.row.timeline.queuedLabel }}</span>
              </div>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="230" fixed="right" align="right">
          <template #default="scope">
            <el-button type="primary" link size="small" @click="goToDetail(scope.row.id)">控制台</el-button>
            <el-button link size="small" @click="rerun(scope.row)">再来一次</el-button>
            <el-button v-if="canResume(scope.row)" type="warning" link size="small" @click="resume(scope.row)">续训</el-button>
            <el-button v-if="canStop(scope.row)" type="danger" link size="small" @click="deleteJob(scope.row.id)">停止</el-button>
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
import { isAdmin, roles, userId } from '../../stores/session'
import { canCancelJob } from '../../jobPermissions'
import { displayJobOwner } from '../../jobOwner'
import { finishedLabel, formatDateTime, jobTimeline, originLabel } from '../../jobTimeline'
import { copyToClipboard } from '../../clipboard'
import { cacheQueryForJob } from '../../platformLimits'

const router = useRouter()
const scope = ref('mine')
const searchKeyword = ref('')
const statusFilter = ref('')
const loading = ref(false)
const jobs = ref([])
const submitterNamesByID = ref(new Map())
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
    created_at: job.createdAt || job.created_at,
    started_at: job.startedAt || job.started_at,
    finished_at: job.finishedAt || job.finished_at,
    // Queue wait and training time are separate facts; a single span measured
    // from submission reported queue wait as training time.
    timeline: jobTimeline(job),
    last_observed_at: job.lastObservedAt || job.last_observed_at,
    submissionOrigin: job.submissionOrigin || job.submission_origin || 'portal',
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

const loadSubmitterDirectory = async () => {
  if (!isAdmin.value) return
  try {
    const users = await apiGet('/api/v1/users')
    submitterNamesByID.value = new Map(
      (users || [])
        .filter((user) => user?.id && user?.username)
        .map((user) => [user.id, user.username])
    )
  } catch {
    // Job visibility remains available when the optional display-name lookup
    // is unavailable; ownerLabel falls back to a short opaque subject.
    submitterNamesByID.value = new Map()
  }
}

const ownerLabel = (job) => displayJobOwner(job.userId, userId.value, submitterNamesByID.value)

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

const ACTIVE_STATES = ['QUEUED', 'PROVISIONING', 'RUNNING', 'RECOVERING', 'SUBMITTED', 'ADMITTED']
const TERMINAL_STATES = ['SUCCEEDED', 'FAILED', 'CANCELED', 'TIMED_OUT']

const summaryCards = computed(() => {
  const items = scopedJobs.value
  const count = (predicate) => items.filter(predicate).length
  const queued = count((j) => j.status === 'QUEUED')
  return [
    {
      label: '进行中', value: count((j) => ACTIVE_STATES.includes(j.status)),
      tone: 'text-blue-400', dot: 'bg-blue-400',
      hint: queued > 0 ? `其中 ${queued} 个在排队` : '没有任务在排队',
    },
    { label: '已成功', value: count((j) => j.status === 'SUCCEEDED'), tone: 'text-emerald-400', dot: 'bg-emerald-400', hint: '结果可在“我的训练结果”查看' },
    { label: '失败', value: count((j) => j.status === 'FAILED'), tone: 'text-red-400', dot: 'bg-red-400', hint: '打开控制台查看失败日志' },
    {
      label: '占用 GPU',
      value: items.filter((j) => ACTIVE_STATES.includes(j.status)).reduce((total, j) => total + j.total_gpus, 0),
      tone: 'text-slate-100', dot: 'bg-slate-400', hint: '仅统计进行中的任务',
    },
  ]
})

const STATUS_LABELS = {
  SUBMITTED: '已提交', VALIDATING: '校验中', QUEUED: '排队中', ADMITTED: '已准入',
  PROVISIONING: '启动中', RUNNING: '运行中', RECOVERING: '恢复中', SUCCEEDED: '已成功', FAILED: '失败',
  CANCELING: '取消中', CANCELED: '已取消', TIMED_OUT: '已超时', DELETING: '清理中', UNKNOWN: '未知'
}
const statusLabel = (status) => STATUS_LABELS[status] || status
const getStatusType = (status) => {
  switch (status) {
    case 'RUNNING':
    case 'RECOVERING': return 'primary'
    case 'SUCCEEDED': return 'success'
    case 'FAILED':
    case 'TIMED_OUT': return 'danger'
    case 'CANCELED': return 'info'
    default: return 'warning'
  }
}

const copyValue = async (value) => {
  if (await copyToClipboard(value)) ElMessage.success('已复制任务 ID')
  else ElMessage.warning('浏览器阻止了剪贴板访问，请手动复制')
}

const goToDetail = (id) => {
  router.push(`/job/detail/${id}`)
}

const lastJob = computed(() => scopedJobs.value.find((job) => job.isMine) || null)

// A run can only be continued from its own managed result directory, which the
// platform creates per job under the selected output space.
const canResume = (job) => TERMINAL_STATES.includes(job.status) && Boolean(job.spec?.output?.space)
const canStop = (job) => canCancelJob(job, { userId: userId.value, roles: roles.value })

const rerun = (job) => {
  router.push({
    path: '/job/create',
    query: {
      name: job.name,
      image: job.spec?.image,
      entrypoint: job.entrypoint,
      ...cacheQueryForJob(job),
    },
  })
}

const resume = (job) => {
  const base = String(job.spec?.output?.relativePath || '').replace(/\/$/, '')
  router.push({
    path: '/job/create',
    query: {
      name: job.name,
      image: job.spec?.image,
      entrypoint: job.entrypoint,
      ...cacheQueryForJob(job),
      checkpointPath: base ? `${base}/${job.id}` : job.id,
    },
  })
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
  loadSubmitterDirectory()
  fetchJobs()
  refreshTimer = window.setInterval(fetchJobs, 5000)
})

onUnmounted(() => window.clearInterval(refreshTimer))
</script>
