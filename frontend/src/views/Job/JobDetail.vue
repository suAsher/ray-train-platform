<template>
  <div v-if="jobDetail" class="space-y-6">
    <!-- Top Summary Banner with Sleek Dark Glassmorphism -->
    <div class="bg-gradient-to-r from-[#111625] via-[#151C2E] to-[#111625] p-6 rounded-2xl border border-slate-800/80 shadow-2xl backdrop-blur-xl relative overflow-hidden">
      <div class="absolute -right-10 -top-10 w-48 h-48 bg-blue-600/10 rounded-full blur-3xl pointer-events-none"></div>
      
      <div class="flex items-center justify-between relative z-10">
        <div class="space-y-2">
          <div class="flex items-center gap-3">
            <h3 class="text-2xl font-bold text-white tracking-tight">{{ jobDetail.name }}</h3>
            <div :class="['flex items-center gap-2 px-3 py-1 rounded-full text-xs font-semibold', statusClass(jobDetail.status)]">
              <span :class="['w-2 h-2 rounded-full', statusDotClass(jobDetail.status)]"></span>
              {{ jobDetail.status }}
            </div>
            <span class="text-xs font-mono px-3 py-1 bg-blue-500/10 text-blue-400 rounded-full border border-blue-500/20 font-semibold flex items-center gap-1.5">
              <el-icon><Cpu /></el-icon> {{ gpuSummary }}
            </span>
          </div>

          <div class="flex flex-wrap items-center gap-x-6 gap-y-2 font-mono text-xs text-slate-400">
            <span class="inline-flex items-center gap-1">
              任务 ID: <code class="text-slate-200">{{ jobDetail.id }}</code>
              <el-button link size="small" icon="DocumentCopy" title="复制任务 ID" @click="copyValue(jobDetail.id)" />
            </span>
            <span>提交: <span class="text-slate-300">{{ formatDateTime(timeline.submittedAt) }}</span></span>
            <span v-if="timeline.isWaiting" class="text-amber-400">排队等待 GPU: {{ timeline.queuedLabel }}</span>
            <template v-else>
              <span>开始: <span class="text-slate-300">{{ formatDateTime(timeline.startedAt) }}</span></span>
              <span>结束: <span class="text-slate-300">{{ finishedLabel(timeline.finishedAt) }}</span></span>
              <span>训练时长: <span class="font-bold" :class="timeline.isRunning ? 'text-blue-400' : 'text-amber-400'">{{ timeline.runningLabel }}</span></span>
              <span v-if="timeline.queuedSeconds" class="text-slate-500">排队 {{ timeline.queuedLabel }}</span>
            </template>
          </div>
          <div class="font-mono text-xs text-slate-400">
            入口: <code class="rounded border border-slate-800 bg-slate-900/80 px-2 py-0.5 text-emerald-400">{{ jobDetail.entrypoint }}</code>
          </div>
        </div>

        <div class="flex items-center gap-3">
          <el-button v-if="canResumeFromCheckpoint" type="warning" plain class="!rounded-xl" @click="resumeFromCheckpoint">续训（从此结果继续）</el-button>
          <el-button plain class="!rounded-xl" @click="rerunJob">再来一次</el-button>
          <el-button plain class="!rounded-xl" icon="DocumentCopy" @click="showCli = true">命令行提交</el-button>
          <el-button
            v-if="showRayDashboard"
            type="primary"
            class="!rounded-xl shadow-lg shadow-blue-600/20"
            icon="Link"
            :loading="dashboardOpening"
            title="仅在本任务的 RayCluster 运行期间可用"
            @click="openRayDashboard"
          >Ray Dashboard</el-button>
          <el-button type="danger" plain class="!rounded-xl" icon="CircleClose" @click="cancelJob">停止训练</el-button>
        </div>
      </div>
    </div>

    <el-dialog v-model="showCli" title="用 spk-rayjob 提交同样的任务" width="min(720px, 94vw)">
      <p class="mb-4 text-sm leading-6 text-slate-400">
        网页和命令行提交的是同一份契约。把下面这条命令粘到装了 <code>spk-rayjob</code> 的机器上即可复现这个任务；
        想反复迭代，可在代码目录执行 <code>spk-rayjob init</code> 把这些默认值写进 <code>.spk-rayjob.yaml</code>。
      </p>
      <CopyBlock :text="cliCommand" wrap />
    </el-dialog>

    <!-- Main Tabs Section -->
    <el-tabs v-model="activeTab" type="border-card" class="!bg-[#131826] !border-slate-800/80 !rounded-2xl shadow-2xl">
      
      <!-- TAB 1: MULTI-NODE AGGREGATED LOG CENTER -->
      <el-tab-pane label="🔥 实时聚合日志" name="aggregated-logs">
        <div class="p-4 space-y-4">
          <el-alert v-if="logsError" :title="logsError" type="warning" show-icon :closable="false" class="!rounded-xl" />
          <el-alert v-else title="日志流说明" type="info" :closable="false" class="!rounded-xl">
            <template #default>
              <span><b>任务提交器</b>负责代码准备和启动；<b>Ray Head</b>负责集群调度和 Dashboard；<b>训练 Worker</b>才是用户训练代码、Loss、NCCL 和 Checkpoint 的主要输出。一个 8 卡 Worker Pod 内可同时包含 8 个 rank，日志流数不等于 GPU 数。</span>
            </template>
          </el-alert>
          
          <!-- Rank Cards Bar -->
          <div class="grid grid-cols-4 gap-3">
            <div 
              v-for="rank in rankCards" 
              :key="rank.id"
              @click="selectedRank = rank.id"
              :class="[
                'p-3 rounded-xl border transition-all cursor-pointer flex items-center justify-between',
                selectedRank === rank.id 
                  ? 'bg-blue-600/15 border-blue-500/60 shadow-lg shadow-blue-600/10' 
                  : 'bg-slate-900/60 border-slate-800/80 hover:border-slate-700'
              ]"
            >
              <div class="space-y-0.5">
                <div class="text-xs font-bold text-slate-200 font-mono">{{ rank.label }}</div>
                <div class="text-[11px] text-slate-400">{{ rank.sub }}</div>
              </div>
              <span :class="['w-2 h-2 rounded-full', rank.active ? 'bg-emerald-400 animate-pulse' : 'bg-slate-600']"></span>
            </div>
          </div>

          <!-- Toolbar: Filter Buttons & Search -->
          <div class="flex justify-between items-center bg-slate-950 p-3.5 rounded-xl border border-slate-800/80 gap-4 flex-wrap">
            <div class="flex items-center gap-3">
              <span class="text-xs text-slate-400 font-semibold">日志类型短路过滤:</span>
              <div class="flex items-center gap-2">
                <el-button 
                  v-for="type in logTypes" 
                  :key="type.value"
                  :type="currentLogType === type.value ? type.btnType : ''"
                  size="small"
                  class="!rounded-lg"
                  @click="currentLogType = type.value"
                >
                  {{ type.label }}
                </el-button>
              </div>
            </div>

            <div class="flex items-center gap-3">
              <el-input 
                v-model="logKeyword" 
                placeholder="搜索日志 (支持 Loss, Error, Rank)..." 
                style="width: 260px" 
                prefix-icon="Search" 
                clearable 
                size="small" 
              />
              <el-checkbox v-model="autoScroll" class="text-xs text-slate-400">自动滚动底端</el-checkbox>
              <el-button size="small" icon="Download" @click="downloadLogs">导出日志</el-button>
            </div>
          </div>

          <div v-if="logsLoaded" class="flex items-center justify-between rounded-xl border border-slate-800/80 bg-slate-950/70 px-4 py-2.5 text-xs text-slate-400">
            <span>已加载 <b class="font-mono text-slate-200">{{ rawLogs.length }}</b> 条日志<span v-if="hasOlderLogs">，还有更早日志</span></span>
            <el-button
              v-if="hasOlderLogs"
              size="small"
              plain
              :loading="logsLoadingOlder"
              @click="loadOlderLogs"
            >加载更早日志</el-button>
            <span v-else class="text-slate-500">已到达任务日志起点</span>
          </div>

          <!-- Log Console Display -->
          <div ref="logConsoleRef"
            class="bg-[#070A10] p-5 rounded-xl border border-slate-800/90 font-mono text-xs text-slate-300 h-[580px] overflow-y-auto space-y-2 select-text"
          >
            <el-empty
              v-if="filteredLogs.length === 0"
              :description="logsError ? '日志服务暂时不可达，平台正在自动重试' : '当前查询范围没有日志；任务结束后由 Loki 保留日志'"
            />
            <div v-else
              v-for="(log, idx) in filteredLogs" 
              :key="idx" 
              class="hover:bg-slate-900/80 rounded px-2 py-1 flex items-start gap-3 leading-relaxed transition-colors border-l-2 border-transparent hover:border-blue-500"
            >
              <span class="text-slate-600 select-none text-[11px] w-8 shrink-0">[{{ idx + 1 }}]</span>
              
              <span :class="['px-2 py-0.5 text-[10px] rounded shrink-0 font-bold shadow-sm', getRankTagClass(log.node)]">
                {{ log.node }}
              </span>

              <span :class="['flex-1 break-all', getLogLineClass(log.text)]">
                {{ log.text }}
              </span>
            </div>
          </div>

        </div>
      </el-tab-pane>

      <!-- TAB 2: TRAINING METRICS & LOSS CURVE -->
      <el-tab-pane label="📈 Loss 收敛曲线与指标" name="metrics">
        <div class="p-6 space-y-6">
          <el-alert v-if="metricsError" :title="metricsError" type="warning" show-icon :closable="false" class="!rounded-xl" />
          <el-alert v-if="experimentError" :title="experimentError" type="info" show-icon :closable="false" class="!rounded-xl" />
          <div v-if="experiment?.run" class="flex flex-wrap items-center gap-3 text-xs text-slate-400">
            <span class="rounded-full border border-emerald-500/30 bg-emerald-500/10 px-3 py-1 text-emerald-300">MLflow {{ experiment.run.status || 'RUNNING' }}</span>
            <span>实验：<code class="text-slate-200">{{ experiment.experimentName }}</code></span>
            <span>Run：<code class="text-slate-200">{{ experiment.run.id }}</code></span>
          </div>
          <!-- Metric Stat Cards -->
          <div class="grid grid-cols-4 gap-5">
            <div class="bg-slate-900/60 p-4 rounded-xl border border-slate-800/80">
              <span class="text-xs text-slate-400">当前 Training Loss</span>
              <p class="text-2xl font-bold text-emerald-400 font-mono mt-1">{{ formatMetric(trainingLoss) }}</p>
              <span class="text-[11px] text-slate-500">MLflow 训练指标</span>
            </div>

            <div class="bg-slate-900/60 p-4 rounded-xl border border-slate-800/80">
              <span class="text-xs text-slate-400">集群吞吐 (Throughput)</span>
              <p class="text-2xl font-bold text-blue-400 font-mono mt-1">{{ metrics?.throughput || '—' }}</p>
              <span class="text-[11px] text-slate-500">Prometheus 指标</span>
            </div>

            <div class="bg-slate-900/60 p-4 rounded-xl border border-slate-800/80">
              <span class="text-xs text-slate-400">学习率 (Learning Rate)</span>
              <p class="text-2xl font-bold text-amber-400 font-mono mt-1">{{ formatMetric(learningRate) }}</p>
              <span class="text-[11px] text-slate-500">MLflow 训练指标</span>
            </div>

            <div class="bg-slate-900/60 p-4 rounded-xl border border-slate-800/80">
              <span class="text-xs text-slate-400">当前 Epoch 进度</span>
              <p class="text-2xl font-bold text-purple-400 font-mono mt-1">{{ formatMetric(currentEpoch) }}</p>
              <span class="text-[11px] text-slate-500">MLflow 训练指标</span>
            </div>
          </div>

          <!-- Dynamic SVG Loss Convergence Graph -->
          <div class="bg-[#070A10] p-6 rounded-xl border border-slate-800/80 space-y-4">
            <div class="flex justify-between items-center">
              <h4 class="text-xs font-bold text-slate-200 uppercase tracking-wider">训练指标曲线</h4>
              <span class="text-xs font-mono text-slate-500">由训练脚本上报到 MLflow</span>
            </div>
            <div v-if="lossSeries?.points?.length" class="space-y-3">
              <svg viewBox="0 0 640 220" class="h-64 w-full overflow-visible" role="img" aria-label="Training loss curve">
                <line x1="0" y1="220" x2="640" y2="220" stroke="#334155" stroke-width="1" />
                <line x1="0" y1="0" x2="0" y2="220" stroke="#334155" stroke-width="1" />
                <polyline :points="lossSparkline" fill="none" stroke="#34d399" stroke-width="3" stroke-linejoin="round" stroke-linecap="round" />
              </svg>
              <div class="flex justify-between text-xs text-slate-500">
                <span>{{ lossSeries.key }}</span>
                <span>{{ lossSeries.points.length }} 个记录点</span>
              </div>
            </div>
            <el-empty v-else description="任务尚未上报 Loss；训练代码需在 rank 0 调用 mlflow.log_metric" />
          </div>
        </div>
      </el-tab-pane>

      <!-- TAB 3: TRAINING ARTIFACTS -->
      <el-tab-pane label="💾 训练产物" name="artifacts">
        <div class="p-6">
          <JobArtifactBrowser :job-id="jobDetail.id" />
        </div>
      </el-tab-pane>

      <!-- TAB 4: POD TOPOLOGY -->
      <el-tab-pane label="📦 Pod 副本与节点拓扑" name="topology">
        <div class="p-6 space-y-4">
          <div v-if="runtimePods.length" class="space-y-3">
            <div
              v-for="pod in runtimePods"
              :key="pod.name"
              class="p-4 bg-slate-900/60 rounded-xl border border-slate-800 flex items-center justify-between"
            >
              <div class="flex items-center gap-4 font-mono">
                <el-icon :size="22" :class="pod.gpu_assigned === 0 ? 'text-amber-400' : 'text-blue-400'"><Platform /></el-icon>
                <div>
                  <div class="text-sm font-bold text-white">{{ pod.name }}</div>
                  <div class="text-xs text-slate-400 mt-0.5">{{ pod.role }} | Pod IP: {{ pod.podIp || '尚未分配' }}</div>
                </div>
              </div>

              <div class="flex items-center gap-6 text-xs font-mono">
                <div>宿主机: <span class="text-slate-200">{{ pod.nodeName || '等待调度' }}</span></div>
                <div>申请显卡: <span class="text-blue-400 font-bold">{{ pod.gpuRequested }} 卡</span></div>
                <el-tag type="success" size="small">{{ pod.phase }}</el-tag>
              </div>
            </div>
          </div>
          <el-empty v-else description="Kubernetes Pod 拓扑尚未同步" />
        </div>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { finishedLabel, formatDateTime, jobTimeline } from '../../jobTimeline'
import CopyBlock from '../../components/CopyBlock.vue'
import { copyToClipboard } from '../../clipboard'
import { apiGet, apiPost } from '../../api/client'
import JobArtifactBrowser from '../../components/JobArtifactBrowser.vue'
import { userId } from '../../stores/session'
import { canOpenRayDashboard, jobDashboardAccessPath } from '../../jobDashboard'
import { buildLogStreamCards } from '../../jobLogStreams'
import { logPagePath, mergeLogEntries, normalizeLogPage } from '../../jobLogPagination'
import { createSingleFlight, nextLogRequest } from '../../jobLogPolling'
import { latestMetric, metricSeries, sparklinePoints } from '../../mlflowExperiment'
import { cacheQueryForJob } from '../../platformLimits'
import { equivalentSubmitCommandForJob } from '../../submission'

const route = useRoute()
const router = useRouter()
const activeTab = ref('aggregated-logs')
const selectedRank = ref('ALL')
const currentLogType = ref('ALL')
const logKeyword = ref('')
const autoScroll = ref(true)
const logConsoleRef = ref(null)

const jobDetail = ref(null)
const metrics = ref(null)
const experiment = ref(null)
const runtimePods = ref([])
const logsError = ref('')
const metricsError = ref('')
const experimentError = ref('')
const dashboardOpening = ref(false)

const rawLogs = ref([])
const logsLoaded = ref(false)
const hasOlderLogs = ref(false)
const olderLogCursor = ref('')
const followLogCursor = ref('')
const olderPagesLoaded = ref(false)
const logsLoadingOlder = ref(false)
const nowTick = ref(new Date().toISOString())

const terminalStates = new Set(['SUCCEEDED', 'FAILED', 'CANCELED', 'TIMED_OUT'])
const canResumeFromCheckpoint = computed(() => (
  Boolean(jobDetail.value?.id) &&
  jobDetail.value?.userId === userId.value &&
  terminalStates.has(jobDetail.value?.status)
))
const showRayDashboard = computed(() => canOpenRayDashboard(jobDetail.value))
const trainingLoss = computed(() => latestMetric(experiment.value, ['train_loss', 'training_loss', 'loss']))
const learningRate = computed(() => latestMetric(experiment.value, ['learning_rate', 'lr']))
const currentEpoch = computed(() => latestMetric(experiment.value, ['epoch', 'train_epoch']))
const lossSeries = computed(() => metricSeries(experiment.value, ['train_loss', 'training_loss', 'loss']))
const lossSparkline = computed(() => sparklinePoints(lossSeries.value?.points, 640, 220))
const formatMetric = value => value == null ? '—' : Number(value).toLocaleString('zh-CN', { maximumSignificantDigits: 6 })

const rankCards = computed(() => {
  const nodes = [...new Set(rawLogs.value.map(log => log.node).filter(Boolean))]
  return buildLogStreamCards(nodes, jobDetail.value?.total_gpus || 0)
})

const logTypes = [
  { label: '全部日志', value: 'ALL', btnType: 'primary' },
  { label: 'ERROR 报错', value: 'ERROR', btnType: 'danger' },
  { label: 'Loss 记录', value: 'LOSS', btnType: 'success' },
  { label: 'Checkpoint', value: 'CHECKPOINT', btnType: 'warning' },
  { label: 'NCCL 通信', value: 'NCCL', btnType: 'info' }
]

const filteredLogs = computed(() => {
  return rawLogs.value.filter(l => {
    const matchRank = selectedRank.value === 'ALL' || 
      selectedRank.value === l.node

    const matchType = currentLogType.value === 'ALL' ||
      (currentLogType.value === 'ERROR' && l.text.toLowerCase().includes('error')) ||
      (currentLogType.value === 'LOSS' && l.text.includes('Loss')) ||
      (currentLogType.value === 'CHECKPOINT' && l.text.includes('Checkpoint')) ||
      (currentLogType.value === 'NCCL' && l.text.includes('NCCL'))

    const matchKeyword = !logKeyword.value || l.text.toLowerCase().includes(logKeyword.value.toLowerCase())

    return matchRank && matchType && matchKeyword
  })
})

const getRankTagClass = (node) => {
  const normalized = String(node || '').toLowerCase()
  if (normalized.includes('-head-')) return 'bg-amber-500/20 text-amber-300 border border-amber-500/30'
  if (normalized.includes('worker')) return 'bg-blue-500/20 text-blue-300 border border-blue-500/30'
  return 'bg-emerald-500/20 text-emerald-300 border border-emerald-500/30'
}

const getLogLineClass = (text) => {
  if (text.includes('Error')) return 'text-rose-400 font-bold'
  if (text.includes('Loss')) return 'text-emerald-400 font-semibold'
  if (text.includes('Checkpoint')) return 'text-purple-300'
  if (text.includes('NCCL')) return 'text-blue-400'
  return 'text-slate-300'
}

const downloadLogs = () => {
  const content = rawLogs.value.map(log => `${log.timestamp || ''}\t${log.node || ''}\t${log.text}`).join('\n')
  const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `${route.params.id}.log`
  anchor.click()
  URL.revokeObjectURL(url)
}

const scrollLogsToBottom = async () => {
  if (!autoScroll.value) return
  await nextTick()
  if (logConsoleRef.value) logConsoleRef.value.scrollTop = logConsoleRef.value.scrollHeight
}

const resetLogState = () => {
  rawLogs.value = []
  logsLoaded.value = false
  logsError.value = ''
  hasOlderLogs.value = false
  olderLogCursor.value = ''
  followLogCursor.value = ''
  olderPagesLoaded.value = false
  logsLoadingOlder.value = false
  selectedRank.value = 'ALL'
}

const applyLatestLogPage = async (response, initialTail) => {
  const page = normalizeLogPage(response)
  rawLogs.value = mergeLogEntries(rawLogs.value, page.logs)
  if (initialTail) {
    hasOlderLogs.value = page.hasMore
    olderLogCursor.value = page.nextCursor
    olderPagesLoaded.value = true
  }
  await scrollLogsToBottom()
  return page
}

const loadOlderLogs = async () => {
  if (!hasOlderLogs.value || !olderLogCursor.value || logsLoadingOlder.value) return
  logsLoadingOlder.value = true
  const consoleElement = logConsoleRef.value
  const previousHeight = consoleElement?.scrollHeight || 0
  const previousTop = consoleElement?.scrollTop || 0
  const requestJobId = String(route.params.id)
  try {
    const response = await apiGet(logPagePath(requestJobId, {
      limit: 2000,
      direction: 'backward',
      cursor: olderLogCursor.value,
    }))
    if (requestJobId !== String(route.params.id)) return
    const page = normalizeLogPage(response)
    rawLogs.value = mergeLogEntries(rawLogs.value, page.logs)
    hasOlderLogs.value = page.hasMore
    olderLogCursor.value = page.nextCursor
    await nextTick()
    if (consoleElement) consoleElement.scrollTop = previousTop + consoleElement.scrollHeight - previousHeight
  } catch (error) {
    ElMessage.warning(error.message || '加载更早日志失败')
  } finally {
    logsLoadingOlder.value = false
  }
}

const normalizeDetail = (job) => {
  const spec = job.spec || {}
  const resources = spec.resources || {}
  return {
    ...job,
    name: spec.name || job.name,
    status: job.observedState || 'UNKNOWN',
    entrypoint: [...(spec.entrypoint?.command || []), ...(spec.entrypoint?.args || [])].join(' '),
    worker_replicas: resources.workerReplicas || 0,
    gpus_per_worker: resources.gpusPerWorker || 0,
    total_gpus: (resources.workerReplicas || 0) * (resources.gpusPerWorker || 0),
    pod_topology: job.podTopology || []
  }
}

const timeline = computed(() => jobTimeline(jobDetail.value || {}, nowTick.value))

const statusClass = (status) => {
  if (status === 'SUCCEEDED') return 'bg-emerald-500/10 border border-emerald-500/30 text-emerald-400'
  if (['FAILED', 'CANCELED', 'TIMED_OUT'].includes(status)) return 'bg-rose-500/10 border border-rose-500/30 text-rose-400'
  return 'bg-blue-500/10 border border-blue-500/30 text-blue-400'
}

const statusDotClass = (status) => {
  if (status === 'SUCCEEDED') return 'bg-emerald-400'
  if (['FAILED', 'CANCELED', 'TIMED_OUT'].includes(status)) return 'bg-rose-400'
  return 'bg-blue-400 animate-pulse'
}

const refreshLogs = createSingleFlight(async () => {
  const requestJobId = String(route.params.id)
  const initialTail = rawLogs.value.length === 0
  const request = nextLogRequest(rawLogs.value, followLogCursor.value)
  try {
    const response = await apiGet(logPagePath(requestJobId, request))
    if (requestJobId !== String(route.params.id)) return
    const page = await applyLatestLogPage(response, initialTail)
    if (request.direction === 'forward' && page.nextCursor) {
      followLogCursor.value = page.nextCursor
    } else if (initialTail && rawLogs.value.length) {
      followLogCursor.value = nextLogRequest(rawLogs.value).cursor
    }
    logsLoaded.value = true
    logsError.value = ''
  } catch (error) {
    if (requestJobId !== String(route.params.id)) return
    logsError.value = rawLogs.value.length
      ? '日志刷新暂时失败，平台正在自动重试；已加载的日志仍可查看。'
      : '日志服务暂时不可达，平台正在自动重试。'
  }
})

const fetchDetail = createSingleFlight(async () => {
  const id = String(route.params.id)
  try {
    const detail = await apiGet(`/api/v1/jobs/${id}`)
    if (id !== String(route.params.id)) return
    jobDetail.value = normalizeDetail(detail)
  } catch (error) {
    ElMessage.error(error.message || '无法读取任务详情')
    return
  }

  const [runtimeResult, metricResult, experimentResult] = await Promise.allSettled([
    apiGet(`/api/v1/jobs/${id}/runtime`),
    apiGet(`/api/v1/jobs/${id}/metrics`),
    apiGet(`/api/v1/jobs/${id}/experiment`)
  ])
  if (id !== String(route.params.id)) return
  runtimePods.value = runtimeResult.status === 'fulfilled' ? runtimeResult.value?.pods || [] : []
  metricsError.value = metricResult.status === 'rejected' ? 'Prometheus / DCGM 指标服务尚未接入或暂时不可达。' : ''
  metrics.value = metricResult.status === 'fulfilled' ? metricResult.value : null
  experimentError.value = experimentResult.status === 'rejected' ? 'MLflow 暂时不可达；训练本身不会因此停止。' : ''
  experiment.value = experimentResult.status === 'fulfilled' ? experimentResult.value : null
})

const cancelJob = async () => {
  try {
    await apiPost(`/api/v1/jobs/${route.params.id}/cancel`, {})
    ElMessage.success('已提交停止请求')
    await fetchDetail()
  } catch (error) {
    ElMessage.error(error.message || '停止任务失败')
  }
}

const openRayDashboard = async () => {
  const popup = window.open('', '_blank')
  if (!popup) {
    ElMessage.warning('浏览器阻止了新窗口，请允许本站打开新标签页后重试')
    return
  }
  popup.opener = null
  dashboardOpening.value = true
  try {
    const access = await apiPost(jobDashboardAccessPath(route.params.id), {})
    popup.location.replace(access.url)
  } catch (error) {
    popup.close()
    ElMessage.warning(error.message || 'Ray Dashboard 尚未就绪或 RayCluster 已结束')
  } finally {
    dashboardOpening.value = false
  }
}

const resumeFromCheckpoint = () => {
  const parent = String(jobDetail.value?.spec?.output?.relativePath || '').trim()
  const checkpointPath = [parent, jobDetail.value?.id].filter(Boolean).join('/')
  // Carry the previous run's shape so continuing a training run is one click
  // rather than retyping the image, command and scale.
  router.push({
    path: '/job/create',
    query: {
      from: 'checkpoint',
      checkpointPath,
      name: jobDetail.value?.name,
      image: jobDetail.value?.spec?.image,
      entrypoint: jobDetail.value?.entrypoint,
      ...cacheQueryForJob(jobDetail.value),
    },
  })
}

const rerunJob = () => {
  router.push({
    path: '/job/create',
    query: {
      name: jobDetail.value?.name,
      image: jobDetail.value?.spec?.image,
      entrypoint: jobDetail.value?.entrypoint,
      ...cacheQueryForJob(jobDetail.value),
    },
  })
}

const cliCommand = computed(() => equivalentSubmitCommandForJob(jobDetail.value))

const showCli = ref(false)

const copyValue = async (value) => {
  if (await copyToClipboard(value)) ElMessage.success('已复制任务 ID')
  else ElMessage.warning('浏览器阻止了剪贴板访问，请手动复制')
}

// GPU summary comes from the job itself; the page used to state a fleet size
// that did not match the cluster.
const gpuSummary = computed(() => {
  const resources = jobDetail.value?.spec?.resources || {}
  const workers = resources.workerReplicas || 0
  const gpus = resources.gpusPerWorker || 0
  if (!workers || !gpus) return '未申请 GPU'
  return workers > 1 ? `${workers} 节点 × ${gpus} 卡 = ${workers * gpus} 卡` : `${gpus} 卡（单机）`
})

let refreshTimer
let logRefreshTimer
let clockTimer

watch(() => route.params.id, (nextJobId, previousJobId) => {
  if (String(nextJobId) === String(previousJobId)) return
  jobDetail.value = null
  runtimePods.value = []
  metrics.value = null
  experiment.value = null
  metricsError.value = ''
  experimentError.value = ''
  resetLogState()
  fetchDetail()
  refreshLogs()
})

onMounted(() => {
  fetchDetail()
  refreshLogs()
  refreshTimer = window.setInterval(fetchDetail, 5000)
  logRefreshTimer = window.setInterval(refreshLogs, 5000)
  // A running job's elapsed time is measured against now, so the clock has to
  // advance even between detail refreshes.
  clockTimer = window.setInterval(() => { nowTick.value = new Date().toISOString() }, 1000)
})

onUnmounted(() => {
  window.clearInterval(refreshTimer)
  window.clearInterval(logRefreshTimer)
  window.clearInterval(clockTimer)
})
</script>
