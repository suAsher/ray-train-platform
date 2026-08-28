<template>
  <section class="space-y-3" aria-labelledby="worker-performance-title">
    <div>
      <h5 id="worker-performance-title" class="text-sm font-semibold text-slate-100">Worker 性能明细</h5>
      <p class="mt-1 text-xs text-slate-500">每行对应一个训练 Worker；指标缺失时不会按 0 计算。</p>
    </div>

    <div
      class="overflow-x-auto rounded-xl border border-slate-800/80"
      role="region"
      aria-label="训练 Worker 性能表"
      tabindex="0"
    >
      <table class="min-w-[1120px] w-full border-collapse text-left text-xs">
        <caption class="sr-only">训练 Worker 的计算、数据、内存和网络指标</caption>
        <thead class="bg-slate-950/90 text-slate-400">
          <tr>
            <th scope="col" class="px-3 py-3 font-semibold">Rank</th>
            <th scope="col" class="px-3 py-3 font-semibold">节点</th>
            <th scope="col" class="px-3 py-3 font-semibold">GPU</th>
            <th scope="col" class="px-3 py-3 font-semibold">状态</th>
            <th scope="col" class="px-3 py-3 font-semibold">Step</th>
            <th scope="col" class="px-3 py-3 font-semibold">GPU 利用率</th>
            <th scope="col" class="px-3 py-3 font-semibold">数据等待</th>
            <th scope="col" class="px-3 py-3 font-semibold">重启</th>
            <th scope="col" class="px-3 py-3 font-semibold">CPU</th>
            <th scope="col" class="px-3 py-3 font-semibold">内存</th>
            <th scope="col" class="px-3 py-3 font-semibold">网络</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-slate-800/80 bg-slate-900/45 text-slate-200">
          <tr v-for="(worker, index) in workers" :key="workerKey(worker, index)" class="hover:bg-slate-800/40">
            <td class="px-3 py-3 font-mono" :title="worker.pod || undefined">{{ displayInteger(worker.rank) }}</td>
            <td class="px-3 py-3 font-mono">{{ displayText(worker.node) }}</td>
            <td class="max-w-36 truncate px-3 py-3 font-mono" :title="worker.gpu || undefined">{{ displayText(worker.gpu) }}</td>
            <td class="px-3 py-3"><span :class="stateClass(worker.state)">{{ displayText(worker.state) }}</span></td>
            <td class="px-3 py-3 font-mono tabular-nums">{{ displayInteger(worker.step) }}</td>
            <td class="px-3 py-3 font-mono tabular-nums">{{ formatPercent(worker.summary?.gpuUtilizationPercent) }}</td>
            <td class="px-3 py-3 font-mono tabular-nums">{{ formatDataWait(worker.summary) }}</td>
            <td class="px-3 py-3 font-mono tabular-nums">{{ displayInteger(worker.restarts) }}</td>
            <td class="px-3 py-3 font-mono tabular-nums">{{ formatCPU(worker.summary?.cpuCores) }}</td>
            <td class="px-3 py-3 font-mono tabular-nums">{{ formatBytes(worker.summary?.memoryWorkingSetBytes) }}</td>
            <td class="px-3 py-3 font-mono tabular-nums">{{ formatNetwork(worker.summary) }}</td>
          </tr>
          <tr v-if="!workers.length">
            <td colspan="11" class="px-4 py-8 text-center text-sm text-slate-500">暂无数据</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<script setup>
defineProps({
  workers: { type: Array, default: () => [] },
})

const noData = '暂无数据'
const finite = (value) => typeof value === 'number' && Number.isFinite(value)
const displayText = (value) => typeof value === 'string' && value.trim() ? value : noData
const displayInteger = (value) => finite(value) ? Math.trunc(value).toLocaleString('zh-CN') : noData
const decimal = (value, digits = 1) => value.toLocaleString('zh-CN', { maximumFractionDigits: digits })
const formatPercent = (value) => finite(value) ? `${decimal(value)}%` : noData
const formatCPU = (value) => finite(value) ? `${decimal(value, 2)} 核` : noData

function formatBytes(value) {
  if (!finite(value)) return noData
  if (value >= 1024 ** 3) return `${decimal(value / 1024 ** 3, 2)} GiB`
  if (value >= 1024 ** 2) return `${decimal(value / 1024 ** 2, 1)} MiB`
  if (value >= 1024) return `${decimal(value / 1024, 1)} KiB`
  return `${decimal(value, 0)} B`
}

function formatNetwork(summary) {
  const values = [summary?.networkReceiveBytesPerSecond, summary?.networkTransmitBytesPerSecond].filter(finite)
  if (!values.length) return noData
  return `${formatBytes(values.reduce((total, value) => total + value, 0))}/s`
}

function formatDataWait(summary) {
  const dataTime = summary?.dataTimeSeconds
  if (!finite(dataTime)) return noData
  const duration = `${decimal(dataTime, 3)} s`
  const stepTime = summary?.stepTimeSeconds
  if (!finite(stepTime) || stepTime <= 0) return duration
  return `${decimal((dataTime / stepTime) * 100)}% · ${duration}`
}

function stateClass(state) {
  const normalized = String(state || '').toLowerCase()
  if (normalized === 'running') return 'rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-1 text-emerald-300'
  if (['failed', 'error'].includes(normalized)) return 'rounded-full border border-rose-500/30 bg-rose-500/10 px-2 py-1 text-rose-300'
  return 'text-slate-300'
}

const workerKey = (worker, index) => `${worker.pod || 'worker'}:${worker.rank ?? index}`
</script>
