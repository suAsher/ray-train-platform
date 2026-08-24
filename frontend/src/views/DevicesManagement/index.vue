<template>
  <div class="space-y-6">
    <header class="panel overflow-hidden p-0">
      <div class="flex flex-wrap items-center justify-between gap-4 border-b border-slate-800/80 bg-gradient-to-r from-blue-500/10 via-slate-950 to-cyan-500/5 p-6">
        <div>
          <h3 class="flex items-center gap-2 text-lg font-bold text-white">
            <el-icon class="text-blue-400"><Monitor /></el-icon> GPU 资源池
          </h3>
          <p class="mt-1 text-xs text-slate-400">调度容量来自 Kubernetes；曲线来自 Prometheus 与 DCGM。最新采样和近 1 分钟平均分开显示。</p>
        </div>
        <div class="flex items-center gap-3">
          <el-tag v-if="autoRefresh" size="small" type="success" effect="plain">实时监控 · 10 秒刷新</el-tag>
          <el-switch v-model="autoRefresh" size="small" />
          <el-button icon="Refresh" class="!rounded-xl" :loading="loading" @click="refreshAll">刷新</el-button>
        </div>
      </div>
      <div class="grid gap-px bg-slate-800/70 sm:grid-cols-2 xl:grid-cols-6">
        <div v-for="card in summary" :key="card.label" class="bg-slate-950/80 px-5 py-4">
          <div class="flex items-center justify-between">
            <p class="text-[11px] font-semibold uppercase tracking-wider text-slate-500">{{ card.label }}</p>
            <span class="h-2 w-2 rounded-full" :class="card.dot" />
          </div>
          <p class="mt-2 font-mono text-2xl font-bold tabular-nums" :class="card.tone">{{ card.value }}</p>
          <p class="mt-1 text-[10px] text-slate-500">{{ card.hint }}</p>
        </div>
      </div>
    </header>

    <el-alert v-if="metricsError" type="warning" show-icon :closable="false">
      <template #title>暂时读不到 GPU 最新指标</template>
      {{ metricsError }} 已保留上一次成功结果；缺失样本不会显示为 0。
    </el-alert>

    <section class="panel space-y-5 p-6">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h3 class="text-base font-bold text-white">GPU 性能趋势</h3>
          <p class="mt-1 text-xs text-slate-400">曲线使用近 1 分钟窗口平均，减少 30 秒采样与数据加载间隙造成的误判。</p>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <el-select v-model="selectedNode" class="!w-44" placeholder="选择 GPU 节点">
            <el-option v-for="node in nodes" :key="node.nodeName" :label="node.nodeName" :value="node.nodeName" />
          </el-select>
          <el-radio-group v-model="selectedWindow" size="small">
            <el-radio-button v-for="item in GPU_TIME_WINDOWS" :key="item.value" :value="item.value">{{ item.label }}</el-radio-button>
          </el-radio-group>
        </div>
      </div>

      <el-alert v-if="historyError" type="warning" show-icon :closable="false" title="历史曲线暂不可用">
        {{ historyError }} 最新卡片仍会继续刷新。
      </el-alert>

      <div v-if="selectedNodeSummary" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div class="metric-brief"><span>近 1 分钟平均</span><b>{{ selectedNodeSummary.averageUtilizationPercent }}%</b></div>
        <div class="metric-brief"><span>显存使用量</span><b>{{ formatGiB(selectedNodeSummary.totalMemoryUsedMib) }} GiB</b></div>
        <div class="metric-brief"><span>节点总功率</span><b>{{ selectedNodeSummary.totalPowerWatts }} W</b></div>
        <div class="metric-brief"><span>最高温度</span><b>{{ selectedNodeSummary.maximumTemperatureCelsius }} °C</b></div>
      </div>

      <el-alert v-if="selectedNodeSummary?.imbalanced" type="warning" show-icon :closable="false">
        <template #title>检测到 8 卡负载不均</template>
        近 1 分钟卡间利用率相差 {{ selectedNodeSummary.utilizationSpread }}%，建议检查 DataLoader、rank 阻塞或通信等待。
      </el-alert>

      <div v-loading="historyLoading" class="grid gap-4 xl:grid-cols-2">
        <GPUTrendChart title="GPU 使用率" description="每条曲线对应一张物理 GPU" unit="%" :series="chartSeries('utilizationPercent')" :minimum="0" :maximum="100" />
        <GPUTrendChart title="显存使用量" description="观察显存爬升和 OOM 风险" unit="GiB" :series="chartSeries('memoryUsedMib')" :minimum="0" :scale="1024" />
        <GPUTrendChart title="GPU 功率" description="功率下降通常对应计算或数据等待" unit="W" :series="chartSeries('powerWatts')" :minimum="0" />
        <GPUTrendChart title="GPU 温度" description="持续高温可能触发降频" unit="°C" :series="chartSeries('temperatureCelsius')" :minimum="20" :maximum="100" />
      </div>
    </section>

    <section v-if="nodes.length" class="space-y-5">
      <article v-for="node in nodes" :key="node.nodeName" class="panel space-y-4 p-6 transition-colors" :class="node.nodeName === selectedNode ? 'border-blue-500/40' : ''">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div class="flex items-center gap-3">
              <span class="font-mono font-bold text-white">{{ node.nodeName }}</span>
              <span v-if="node.model" class="text-xs text-slate-400">{{ node.model }}</span>
            </div>
            <p class="mt-1 text-[11px] text-slate-500">{{ node.devices.length }} 张卡 · 调度已分配 {{ node.allocated }} / {{ node.capacity }}</p>
          </div>
          <div class="flex items-center gap-2">
            <el-tag v-if="node.historySummary?.imbalanced" size="small" type="warning" effect="plain">负载不均</el-tag>
            <el-tag size="small" :type="node.busyCount ? 'primary' : 'info'" effect="plain">{{ node.busyCount }} 卡在计算</el-tag>
            <el-button size="small" plain @click="selectedNode = node.nodeName">查看趋势</el-button>
          </div>
        </div>

        <div v-if="node.devices.length" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <div v-for="device in node.devices" :key="device.uuid" class="rounded-2xl border border-slate-800 bg-slate-950/65 p-4">
            <div class="flex items-center justify-between gap-2">
              <span class="font-mono text-xs font-semibold text-slate-300">GPU {{ device.index }}</span>
              <el-tag v-if="device.freshness.stale" size="small" type="warning" effect="plain">数据延迟</el-tag>
              <span v-else class="h-2 w-2 rounded-full" :class="device.displayUtilization > 5 ? 'bg-emerald-400 shadow-[0_0_10px_#34d399]' : 'bg-slate-600'" />
            </div>
            <div class="mt-3 flex items-end justify-between gap-2">
              <p class="font-mono text-3xl font-bold tabular-nums" :class="utilTone(device.displayUtilization)">
                {{ Math.round(device.displayUtilization) }}<span class="text-sm text-slate-500">%</span>
              </p>
              <div class="text-right text-[10px] text-slate-500">
                <p>近 1 分钟平均</p><p>最新采样 {{ Math.round(device.utilizationPercent) }}%</p>
              </div>
            </div>
            <el-progress class="mt-2" :percentage="Math.min(100, Math.round(device.displayUtilization))" :show-text="false" :stroke-width="6" :color="progressColor(device.displayUtilization)" />

            <dl class="mt-4 space-y-2 font-mono text-[11px] text-slate-400">
              <div class="space-y-1">
                <div class="flex justify-between"><dt>显存占比</dt><dd :class="memoryTone(device)">{{ memoryLabel(device) }}</dd></div>
                <el-progress :percentage="device.memoryPercent" :show-text="false" :stroke-width="4" :color="device.memoryPercent > 90 ? '#f59e0b' : '#38bdf8'" />
              </div>
              <div class="flex justify-between"><dt>温度</dt><dd>{{ Math.round(device.temperatureCelsius) }} °C</dd></div>
              <div class="flex justify-between"><dt>功率</dt><dd>{{ Math.round(device.powerWatts) }} W</dd></div>
              <div class="flex justify-between gap-3"><dt>采样时间</dt><dd class="truncate text-right">{{ sampleAgeLabel(device.freshness) }}</dd></div>
            </dl>
            <div class="mt-3 border-t border-slate-800/80 pt-3">
              <p class="text-[10px] uppercase tracking-wider text-slate-600">所属工作负载</p>
              <p class="mt-1 truncate font-mono text-[10px] text-slate-400" :title="device.podName || '未关联工作负载'">{{ device.podName || '未关联工作负载' }}</p>
            </div>
          </div>
        </div>

        <div v-else class="space-y-2">
          <el-progress :percentage="allocationPercent(node)" :show-text="false" />
          <p class="text-xs text-slate-500">该节点暂无 DCGM 数据，仅显示 Kubernetes 调度容量。</p>
        </div>
      </article>
    </section>
    <el-empty v-else description="暂无 GPU 节点数据" />
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import { apiGet } from '../../api/client'
import { fetchGPUHistory, fetchGPUMetrics } from '../../api/platform'
import GPUTrendChart from '../../components/gpu/GPUTrendChart.vue'
import { GPU_TIME_WINDOWS, metricChartSeries, nodeMetricSummary, normalizeGPUHistory, recentDeviceAverage, sampleFreshness } from '../../gpuMetrics'

const topology = ref(null)
const inventory = ref(null)
const history = ref(normalizeGPUHistory(null))
const metricsError = ref('')
const historyError = ref('')
const loading = ref(false)
const historyLoading = ref(false)
const autoRefresh = ref(true)
const selectedWindow = ref('1h')
const selectedNode = ref('')
let currentTimer
let historyTimer
let historyGeneration = 0

const nodes = computed(() => {
  const devicesByNode = new Map()
  for (const device of inventory.value?.devices || []) {
    if (!devicesByNode.has(device.nodeName)) devicesByNode.set(device.nodeName, [])
    devicesByNode.get(device.nodeName).push(enrichDevice(device))
  }
  const fromTopology = (topology.value?.nodes || []).map((node) => {
    const devices = devicesByNode.get(node.nodeName) || []
    devicesByNode.delete(node.nodeName)
    return buildNode(node.nodeName, node, devices)
  })
  const extra = [...devicesByNode.entries()].map(([nodeName, devices]) => buildNode(nodeName, null, devices))
  return [...fromTopology, ...extra]
})

function enrichDevice(device) {
  const recent = recentDeviceAverage(history.value, device.uuid)
  const memoryTotal = Number(device.memoryTotalMib) || 0
  return {
    ...device,
    displayUtilization: recent ?? (Number(device.utilizationPercent) || 0),
    memoryPercent: memoryTotal ? Math.min(100, Math.round((Number(device.memoryUsedMib) / memoryTotal) * 100)) : 0,
    freshness: sampleFreshness(device.sampledAt),
  }
}

function buildNode(nodeName, topologyNode, devices) {
  const historySummary = nodeMetricSummary(history.value, nodeName)
  return {
    nodeName,
    capacity: topologyNode?.capacity ?? devices.length,
    allocated: topologyNode?.allocated ?? 0,
    available: topologyNode?.available ?? 0,
    devices,
    busyCount: devices.filter((device) => !device.freshness.stale && device.displayUtilization > 5).length,
    model: devices[0]?.model || '',
    historySummary,
  }
}

const summary = computed(() => {
  const devices = nodes.value.flatMap((node) => node.devices)
  const total = inventory.value?.totalGpus ?? nodes.value.reduce((sum, node) => sum + node.capacity, 0)
  const allocated = nodes.value.reduce((sum, node) => sum + node.allocated, 0)
  const active = devices.filter((device) => !device.freshness.stale && device.displayUtilization > 5).length
  const averageUtil = devices.length ? Math.round(devices.reduce((sum, device) => sum + device.displayUtilization, 0) / devices.length) : 0
  const memoryUsed = devices.reduce((sum, device) => sum + (Number(device.memoryUsedMib) || 0), 0)
  const memoryTotal = devices.reduce((sum, device) => sum + (Number(device.memoryTotalMib) || 0), 0)
  const totalPower = Math.round(devices.reduce((sum, device) => sum + (Number(device.powerWatts) || 0), 0))
  return [
    { label: 'GPU 总数', value: total, tone: 'text-slate-100', dot: 'bg-slate-400', hint: `${nodes.value.length} 个节点` },
    { label: '正在计算', value: active, tone: 'text-emerald-400', dot: 'bg-emerald-400', hint: '近 1 分钟平均 > 5%' },
    { label: '调度已分配', value: allocated, tone: 'text-amber-400', dot: 'bg-amber-400', hint: '已被任务或调试环境预留' },
    { label: '平均利用率', value: `${averageUtil}%`, tone: utilTone(averageUtil), dot: 'bg-blue-400', hint: '近 1 分钟平均' },
    { label: '显存使用', value: memoryTotal ? `${formatGiB(memoryUsed)} / ${formatGiB(memoryTotal)}` : '—', tone: 'text-cyan-300', dot: 'bg-cyan-400', hint: 'GiB，整个资源池' },
    { label: '当前总功率', value: `${totalPower} W`, tone: 'text-violet-300', dot: 'bg-violet-400', hint: '全部 GPU 最新采样' },
  ]
})

const selectedNodeSummary = computed(() => selectedNode.value ? nodeMetricSummary(history.value, selectedNode.value) : null)
const chartSeries = (metric) => metricChartSeries(history.value, selectedNode.value, metric)
const allocationPercent = (node) => node.capacity ? Math.round((node.allocated / node.capacity) * 100) : 0
const formatGiB = (mib) => (Number(mib) / 1024).toFixed(Number(mib) >= 10240 ? 0 : 1)
const memoryLabel = (device) => device.memoryTotalMib ? `${formatGiB(device.memoryUsedMib)} / ${formatGiB(device.memoryTotalMib)} GiB` : '—'
const memoryTone = (device) => device.memoryPercent > 90 ? 'text-amber-400' : 'text-slate-300'
const sampleAgeLabel = (freshness) => freshness.ageSeconds === null ? '无有效样本' : freshness.ageSeconds < 60 ? `${freshness.ageSeconds} 秒前` : `${Math.floor(freshness.ageSeconds / 60)} 分钟前`
const utilTone = (value) => value >= 70 ? 'text-emerald-400' : value >= 5 ? 'text-blue-400' : 'text-slate-400'
const progressColor = (value) => value >= 70 ? '#34d399' : value >= 5 ? '#60a5fa' : '#475569'

async function loadHistory() {
  const generation = ++historyGeneration
  historyLoading.value = true
  try {
    // Fetch all GPU nodes in one bounded request. Cards and fleet summaries
    // therefore stay on the same one-minute average even when another node is
    // selected for chart inspection.
    const payload = await fetchGPUHistory(selectedWindow.value)
    if (generation !== historyGeneration) return
    history.value = normalizeGPUHistory(payload)
    historyError.value = ''
  } catch (error) {
    if (generation === historyGeneration) historyError.value = error.message || 'Prometheus 历史查询暂不可用。'
  } finally {
    if (generation === historyGeneration) historyLoading.value = false
  }
}

async function refreshCurrent() {
  loading.value = true
  const [topologyResult, metricsResult] = await Promise.allSettled([apiGet('/api/v1/cluster/topology'), fetchGPUMetrics()])
  if (topologyResult.status === 'fulfilled') topology.value = topologyResult.value
  else if (!topology.value) ElMessage.error(topologyResult.reason?.message || '无法读取 Kubernetes GPU 资源')
  if (metricsResult.status === 'fulfilled') {
    inventory.value = metricsResult.value
    metricsError.value = ''
    if (!selectedNode.value) {
      const busiest = [...(metricsResult.value?.devices || [])].sort((left, right) => Number(right.utilizationPercent) - Number(left.utilizationPercent))[0]
      selectedNode.value = busiest?.nodeName || topology.value?.nodes?.[0]?.nodeName || ''
    }
  } else metricsError.value = metricsResult.reason?.message || 'Prometheus 或 DCGM Exporter 暂不可用。'
  loading.value = false
}

async function refreshAll() {
  await refreshCurrent()
  await loadHistory()
}

watch(selectedWindow, loadHistory)

onMounted(async () => {
  await refreshCurrent()
  await loadHistory()
  currentTimer = window.setInterval(() => { if (autoRefresh.value) refreshCurrent() }, 10000)
  historyTimer = window.setInterval(() => { if (autoRefresh.value) loadHistory() }, 30000)
})

onUnmounted(() => {
  window.clearInterval(currentTimer)
  window.clearInterval(historyTimer)
})
</script>

<style scoped>
.metric-brief {
  @apply flex items-center justify-between rounded-xl border border-slate-800 bg-slate-950/60 px-4 py-3 text-xs text-slate-500;
}
.metric-brief b {
  @apply font-mono text-base text-slate-200;
}
</style>
