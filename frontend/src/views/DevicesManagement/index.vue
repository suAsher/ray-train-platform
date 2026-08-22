<template>
  <div class="space-y-6">
    <div class="panel flex flex-wrap items-center justify-between gap-4 p-6">
      <div>
        <h3 class="flex items-center gap-2 text-lg font-bold text-white">
          <el-icon class="text-blue-400"><Monitor /></el-icon> GPU 资源池
        </h3>
        <p class="mt-1 text-xs text-slate-400">
          调度容量来自 Kubernetes；利用率、显存、温度与功耗来自 DCGM Exporter 实时采集。
        </p>
      </div>
      <div class="flex items-center gap-3">
        <el-tag v-if="autoRefresh" size="small" type="success" effect="plain">每 10 秒自动刷新</el-tag>
        <el-switch v-model="autoRefresh" size="small" />
        <el-button icon="Refresh" class="!rounded-xl" :loading="loading" @click="refresh">刷新</el-button>
      </div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summary" :key="card.label" class="stat-tile">
        <div class="flex items-center justify-between">
          <p class="stat-tile__label">{{ card.label }}</p>
          <span class="h-2 w-2 rounded-full" :class="card.dot" />
        </div>
        <p class="stat-tile__value" :class="card.tone">{{ card.value }}</p>
        <p class="text-[11px] text-slate-500">{{ card.hint }}</p>
      </div>
    </div>

    <el-alert v-if="metricsError" type="warning" show-icon :closable="false">
      <template #title>暂时读不到 GPU 实时指标</template>
      {{ metricsError }} 下方仍会显示 Kubernetes 的调度容量。
    </el-alert>

    <div v-if="nodes.length" class="space-y-5">
      <div v-for="node in nodes" :key="node.nodeName" class="panel space-y-4 p-6">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <span class="font-mono font-bold text-white">{{ node.nodeName }}</span>
            <span v-if="node.model" class="ml-3 text-xs text-slate-400">{{ node.model }}</span>
          </div>
          <div class="flex items-center gap-2">
            <el-tag size="small" :type="node.busyCount ? 'primary' : 'info'" effect="plain">
              {{ node.busyCount }} / {{ node.devices.length || node.capacity }} 卡在计算
            </el-tag>
            <el-tag size="small" type="success" effect="plain">调度已分配 {{ node.allocated }} / {{ node.capacity }}</el-tag>
          </div>
        </div>

        <div v-if="node.devices.length" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <div v-for="device in node.devices" :key="device.uuid" class="rounded-xl border border-slate-800 bg-slate-950/60 p-4">
            <div class="flex items-center justify-between">
              <span class="font-mono text-xs text-slate-300">GPU {{ device.index }}</span>
              <span class="h-2 w-2 rounded-full" :class="device.busy ? 'bg-blue-400' : 'bg-slate-600'" />
            </div>
            <p class="mt-2 font-mono text-2xl font-bold tabular-nums" :class="utilTone(device.utilizationPercent)">
              {{ Math.round(device.utilizationPercent) }}<span class="text-sm text-slate-500">%</span>
            </p>
            <el-progress
              class="mt-1"
              :percentage="Math.min(100, Math.round(device.utilizationPercent))"
              :show-text="false"
              :stroke-width="6"
              :color="progressColor(device.utilizationPercent)"
            />
            <dl class="mt-3 space-y-1 font-mono text-[11px] text-slate-400">
              <div class="flex justify-between">
                <dt>显存</dt>
                <dd :class="memoryTone(device)">{{ memoryLabel(device) }}</dd>
              </div>
              <div class="flex justify-between"><dt>温度</dt><dd>{{ Math.round(device.temperatureCelsius) }} °C</dd></div>
              <div class="flex justify-between"><dt>功耗</dt><dd>{{ Math.round(device.powerWatts) }} W</dd></div>
            </dl>
          </div>
        </div>

        <div v-else class="space-y-1">
          <div class="flex justify-between text-xs text-slate-400">
            <span>调度占用</span><span>{{ allocationPercent(node) }}%</span>
          </div>
          <el-progress :percentage="allocationPercent(node)" :show-text="false" />
          <p class="text-xs text-slate-500">该节点暂无 DCGM 实时数据，仅显示 Kubernetes 调度容量。</p>
        </div>
      </div>
    </div>
    <el-empty v-else description="暂无 GPU 节点数据" />
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'

import { apiGet } from '../../api/client'
import { fetchGPUMetrics } from '../../api/platform'

const topology = ref(null)
const inventory = ref(null)
const metricsError = ref('')
const loading = ref(false)
const autoRefresh = ref(true)
let timer

/**
 * Kubernetes knows the schedulable capacity; DCGM knows what is actually
 * running. Showing only the former made a fully reserved but idle fleet look
 * identical to a saturated one, so the two are merged per node here.
 */
const nodes = computed(() => {
  const devicesByNode = new Map()
  for (const device of inventory.value?.devices || []) {
    if (!devicesByNode.has(device.nodeName)) devicesByNode.set(device.nodeName, [])
    devicesByNode.get(device.nodeName).push(device)
  }
  const fromTopology = (topology.value?.nodes || []).map((node) => {
    const devices = devicesByNode.get(node.nodeName) || []
    devicesByNode.delete(node.nodeName)
    return buildNode(node.nodeName, node, devices)
  })
  // A node reporting GPUs that Kubernetes does not list as schedulable still
  // matters: it is usually cordoned, and hiding it hides real hardware.
  const extra = [...devicesByNode.entries()].map(([nodeName, devices]) => buildNode(nodeName, null, devices))
  return [...fromTopology, ...extra]
})

function buildNode(nodeName, topologyNode, devices) {
  return {
    nodeName,
    capacity: topologyNode?.capacity ?? devices.length,
    allocated: topologyNode?.allocated ?? 0,
    available: topologyNode?.available ?? 0,
    devices,
    busyCount: devices.filter((device) => device.busy).length,
    model: devices[0]?.model || '',
  }
}

const summary = computed(() => {
  const total = inventory.value?.totalGpus ?? nodes.value.reduce((sum, node) => sum + node.capacity, 0)
  const busy = inventory.value?.busyGpus ?? 0
  const allocated = nodes.value.reduce((sum, node) => sum + node.allocated, 0)
  const averageUtil = inventory.value?.devices?.length
    ? Math.round(inventory.value.devices.reduce((sum, device) => sum + device.utilizationPercent, 0) / inventory.value.devices.length)
    : 0
  return [
    { label: 'GPU 总数', value: total, tone: 'text-slate-100', dot: 'bg-slate-400', hint: `${nodes.value.length} 个节点` },
    { label: '正在计算', value: busy, tone: 'text-blue-400', dot: 'bg-blue-400', hint: '利用率高于 5% 的卡' },
    { label: '调度已分配', value: allocated, tone: 'text-amber-400', dot: 'bg-amber-400', hint: '已被任务预留的卡' },
    { label: '平均利用率', value: `${averageUtil}%`, tone: utilTone(averageUtil), dot: 'bg-emerald-400', hint: '全部 GPU 的实时均值' },
  ]
})

const allocationPercent = (node) => (node.capacity ? Math.round((node.allocated / node.capacity) * 100) : 0)

const memoryLabel = (device) => (device.memoryTotalMib
  ? `${Math.round(device.memoryUsedMib / 1024)} / ${Math.round(device.memoryTotalMib / 1024)} GiB`
  : '—')

// Near-full memory is the usual precursor to an out-of-memory crash, so it is
// called out before the job fails.
const memoryTone = (device) => (device.memoryTotalMib && device.memoryUsedMib / device.memoryTotalMib > 0.9
  ? 'text-amber-400'
  : 'text-slate-300')

const utilTone = (value) => {
  if (value >= 70) return 'text-emerald-400'
  if (value >= 5) return 'text-blue-400'
  return 'text-slate-400'
}

const progressColor = (value) => {
  if (value >= 70) return '#34d399'
  if (value >= 5) return '#60a5fa'
  return '#475569'
}

const refresh = async () => {
  loading.value = true
  const [topologyResult, metricsResult] = await Promise.allSettled([
    apiGet('/api/v1/cluster/topology'),
    fetchGPUMetrics(),
  ])
  if (topologyResult.status === 'fulfilled') {
    topology.value = topologyResult.value
  } else if (!topology.value) {
    ElMessage.error(topologyResult.reason?.message || '无法读取 Kubernetes GPU 资源')
  }
  if (metricsResult.status === 'fulfilled') {
    inventory.value = metricsResult.value
    metricsError.value = ''
  } else {
    metricsError.value = metricsResult.reason?.message || 'Prometheus 或 DCGM Exporter 暂不可用。'
  }
  loading.value = false
}

onMounted(() => {
  refresh()
  timer = window.setInterval(() => { if (autoRefresh.value) refresh() }, 10000)
})

onUnmounted(() => window.clearInterval(timer))
</script>
