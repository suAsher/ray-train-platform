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
              <el-icon><Cpu /></el-icon> {{ jobDetail.total_gpus }} 卡 RTX 4090 并行训练
            </span>
          </div>

          <div class="flex items-center gap-6 text-xs font-mono text-slate-400">
            <span>任务 ID: <code class="text-slate-200">{{ jobDetail.id }}</code></span>
            <span>已运行时间: <span class="text-amber-400 font-bold">{{ formatDuration(jobDetail) }}</span></span>
            <span>入口: <code class="text-emerald-400 bg-slate-900/80 px-2 py-0.5 rounded border border-slate-800">{{ jobDetail.entrypoint }}</code></span>
          </div>
        </div>

        <div class="flex items-center gap-3">
          <a v-if="jobDetail.ray_dashboard_url" :href="jobDetail.ray_dashboard_url" target="_blank">
            <el-button type="primary" class="!rounded-xl shadow-lg shadow-blue-600/20" icon="Link">Ray Dashboard (8265)</el-button>
          </a>
          <el-button type="danger" plain class="!rounded-xl" icon="CircleClose" @click="cancelJob">停止训练</el-button>
        </div>
      </div>
    </div>

    <!-- Main Tabs Section -->
    <el-tabs v-model="activeTab" type="border-card" class="!bg-[#131826] !border-slate-800/80 !rounded-2xl shadow-2xl">
      
      <!-- TAB 1: MULTI-NODE AGGREGATED LOG CENTER -->
      <el-tab-pane label="🔥 多节点 24卡 实时聚合日志" name="aggregated-logs">
        <div class="p-4 space-y-4">
          
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

          <!-- Log Console Display -->
          <div 
            ref="logConsoleRef"
            class="bg-[#070A10] p-5 rounded-xl border border-slate-800/90 font-mono text-xs text-slate-300 h-[580px] overflow-y-auto space-y-2 select-text"
          >
            <div 
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
          <!-- Metric Stat Cards -->
          <div class="grid grid-cols-4 gap-5">
            <div class="bg-slate-900/60 p-4 rounded-xl border border-slate-800/80">
              <span class="text-xs text-slate-400">当前 Training Loss</span>
              <p class="text-2xl font-bold text-emerald-400 font-mono mt-1">{{ metrics?.loss || '—' }}</p>
              <span class="text-[11px] text-slate-500">等待训练脚本上报指标</span>
            </div>

            <div class="bg-slate-900/60 p-4 rounded-xl border border-slate-800/80">
              <span class="text-xs text-slate-400">集群吞吐 (Throughput)</span>
              <p class="text-2xl font-bold text-blue-400 font-mono mt-1">{{ metrics?.throughput || '—' }}</p>
              <span class="text-[11px] text-slate-500">Prometheus 指标</span>
            </div>

            <div class="bg-slate-900/60 p-4 rounded-xl border border-slate-800/80">
              <span class="text-xs text-slate-400">学习率 (Learning Rate)</span>
              <p class="text-2xl font-bold text-amber-400 font-mono mt-1">{{ metrics?.learningRate || '—' }}</p>
              <span class="text-[11px] text-slate-500">训练脚本指标</span>
            </div>

            <div class="bg-slate-900/60 p-4 rounded-xl border border-slate-800/80">
              <span class="text-xs text-slate-400">当前 Epoch 进度</span>
              <p class="text-2xl font-bold text-purple-400 font-mono mt-1">{{ metrics?.epoch || '—' }}</p>
              <span class="text-[11px] text-slate-500">训练脚本指标</span>
            </div>
          </div>

          <!-- Dynamic SVG Loss Convergence Graph -->
          <div class="bg-[#070A10] p-6 rounded-xl border border-slate-800/80 space-y-4">
            <div class="flex justify-between items-center">
              <h4 class="text-xs font-bold text-slate-200 uppercase tracking-wider">训练指标曲线</h4>
              <span class="text-xs font-mono text-slate-500">由训练脚本/Prometheus 提供</span>
            </div>
            <el-empty v-if="!metrics" description="当前任务尚未提供可绘制的训练指标" />
          </div>
        </div>
      </el-tab-pane>

      <!-- TAB 3: CHECKPOINTS -->
      <el-tab-pane label="💾 模型的 Checkpoints 产物" name="checkpoints">
        <div class="p-6 space-y-4">
          <div class="flex justify-between items-center">
            <h4 class="text-xs font-bold text-slate-300 uppercase">TOS 对象存储关联挂载点</h4>
            <el-tag type="info" size="small" class="font-mono">{{ jobDetail.spec?.outputUri || '未配置产物 URI' }}</el-tag>
          </div>

          <el-table :data="checkpoints" style="width: 100%" class="!bg-transparent text-xs">
            <el-table-column prop="name" label="Checkpoint 文件名" min-width="220">
              <template #default="scope">
                <span class="font-mono font-bold text-slate-100 flex items-center gap-2">
                  <el-icon class="text-purple-400"><Document /></el-icon> {{ scope.row.name }}
                </span>
              </template>
            </el-table-column>
            <el-table-column prop="step" label="Step / Epoch 节点" width="160" />
            <el-table-column prop="size" label="文件大小" width="120" />
            <el-table-column prop="tos_url" label="TOS URI 路径" min-width="280 font-mono text-slate-400" />
            <el-table-column label="操作" width="140" align="right">
              <template #default>
                <el-button type="primary" link size="small" icon="Download">下载权重</el-button>
              </template>
            </el-table-column>
          </el-table>
        </div>
      </el-tab-pane>

      <!-- TAB 4: POD TOPOLOGY -->
      <el-tab-pane label="📦 Pod 副本与节点拓扑" name="topology">
        <div class="p-6 space-y-4">
          <div v-if="jobDetail.pod_topology.length" class="space-y-3">
            <div 
              v-for="pod in jobDetail.pod_topology" 
              :key="pod.name" 
              class="p-4 bg-slate-900/60 rounded-xl border border-slate-800 flex items-center justify-between"
            >
              <div class="flex items-center gap-4 font-mono">
                <el-icon :size="22" :class="pod.gpu_assigned === 0 ? 'text-amber-400' : 'text-blue-400'"><Platform /></el-icon>
                <div>
                  <div class="text-sm font-bold text-white">{{ pod.name }}</div>
                  <div class="text-xs text-slate-400 mt-0.5">{{ pod.role }} | Pod IP: {{ pod.pod_ip }}</div>
                </div>
              </div>

              <div class="flex items-center gap-6 text-xs font-mono">
                <div>宿主机: <span class="text-slate-200">{{ pod.node_name }}</span></div>
                <div>显卡: <span class="text-blue-400 font-bold">{{ pod.gpu_assigned }} 卡 4090</span></div>
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
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { apiGet, apiPost } from '../../api/client'

const route = useRoute()
const activeTab = ref('aggregated-logs')
const selectedRank = ref('ALL')
const currentLogType = ref('ALL')
const logKeyword = ref('')
const autoScroll = ref(true)
const logConsoleRef = ref(null)

const jobDetail = ref(null)
const metrics = ref(null)

const rawLogs = ref([])

const rankCards = computed(() => {
  const nodes = [...new Set(rawLogs.value.map(log => log.node).filter(Boolean))]
  return [
    { id: 'ALL', label: `${jobDetail.value?.total_gpus || 0} 卡全量聚合`, sub: `${nodes.length} 个日志流`, active: true },
    ...nodes.map(node => ({ id: node, label: node, sub: 'Loki stream', active: true }))
  ]
})

const logTypes = [
  { label: '全部日志', value: 'ALL', btnType: 'primary' },
  { label: 'ERROR 报错', value: 'ERROR', btnType: 'danger' },
  { label: 'Loss 记录', value: 'LOSS', btnType: 'success' },
  { label: 'Checkpoint', value: 'CHECKPOINT', btnType: 'warning' },
  { label: 'NCCL 通信', value: 'NCCL', btnType: 'info' }
]

const checkpoints = ref([])

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
  if (node.includes('Head')) return 'bg-amber-500/20 text-amber-300 border border-amber-500/30'
  if (node.includes('Worker-1')) return 'bg-blue-500/20 text-blue-300 border border-blue-500/30'
  if (node.includes('Worker-2')) return 'bg-purple-500/20 text-purple-300 border border-purple-500/30'
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
    ray_dashboard_url: job.rayDashboardUrl || '',
    pod_topology: job.podTopology || []
  }
}

const formatDuration = (job) => {
  const started = Date.parse(job?.createdAt || '')
  if (!Number.isFinite(started)) return '—'
  const finished = job.finishedAt ? Date.parse(job.finishedAt) : Date.now()
  const seconds = Math.max(0, Math.floor((finished - started) / 1000))
  const hours = String(Math.floor(seconds / 3600)).padStart(2, '0')
  const minutes = String(Math.floor((seconds % 3600) / 60)).padStart(2, '0')
  const remainder = String(seconds % 60).padStart(2, '0')
  return `${hours}:${minutes}:${remainder}`
}

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

const fetchDetail = async () => {
  const id = route.params.id
  try {
    jobDetail.value = normalizeDetail(await apiGet(`/api/v1/jobs/${id}`))
    const logResponse = await apiGet(`/api/v1/jobs/${id}/logs?limit=2000`)
    rawLogs.value = (logResponse.items || []).map(item => ({
      node: item.stream?.pod || item.stream?.container || 'cluster',
      text: item.line,
      timestamp: item.timestamp
    }))
    metrics.value = await apiGet(`/api/v1/jobs/${id}/metrics`)
  } catch (error) {
    if (error.code === 'METRICS_UNAVAILABLE' || error.code === 'METRICS_QUERY_FAILED') {
      metrics.value = null
      return
    }
    ElMessage.error(error.message || '无法读取任务详情')
  }
}

const cancelJob = async () => {
  try {
    await apiPost(`/api/v1/jobs/${route.params.id}/cancel`, {})
    ElMessage.success('已提交停止请求')
    await fetchDetail()
  } catch (error) {
    ElMessage.error(error.message || '停止任务失败')
  }
}

let refreshTimer

onMounted(() => {
  fetchDetail()
  refreshTimer = window.setInterval(fetchDetail, 5000)
})

onUnmounted(() => window.clearInterval(refreshTimer))
</script>
