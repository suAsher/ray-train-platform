<template>
  <section class="space-y-4" aria-labelledby="data-performance-title">
    <div>
      <h5 id="data-performance-title" class="text-sm font-semibold text-slate-100">性能诊断</h5>
      <p class="mt-1 text-xs text-slate-500">诊断仅提供排障建议，不会修改训练参数或运行时配置。</p>
    </div>

    <el-alert
      :type="diagnosis?.severity || 'info'"
      :title="diagnosis?.title || '暂无诊断数据'"
      show-icon
      :closable="false"
    >
      {{ diagnosis?.advice || '暂无数据，等待训练指标采样。' }}
    </el-alert>

    <dl class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <div v-for="card in cards" :key="card.label" class="rounded-xl border border-slate-800/80 bg-slate-900/55 p-4">
        <dt class="text-xs text-slate-400">{{ card.label }}</dt>
        <dd class="mt-1 font-mono text-lg font-semibold tabular-nums text-slate-100">{{ card.value }}</dd>
        <p class="mt-1 text-[11px] text-slate-500">{{ card.hint }}</p>
      </div>
    </dl>

    <p v-if="hasMissingData" class="text-xs text-slate-500">“暂无数据”表示指标未上报或当前时间窗口没有样本，不会按 0 处理。</p>
  </section>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  summary: { type: Object, default: () => ({}) },
  diagnosis: { type: Object, default: null },
})

const noData = '暂无数据'
const finite = (value) => typeof value === 'number' && Number.isFinite(value)
const decimal = (value, digits = 2) => value.toLocaleString('zh-CN', { maximumFractionDigits: digits })
const seconds = (value) => finite(value) ? `${decimal(value, 3)} s` : noData

function bytes(value) {
  if (!finite(value)) return noData
  if (value >= 1024 ** 3) return `${decimal(value / 1024 ** 3)} GiB`
  if (value >= 1024 ** 2) return `${decimal(value / 1024 ** 2)} MiB`
  if (value >= 1024) return `${decimal(value / 1024)} KiB`
  return `${decimal(value, 0)} B`
}

function bytesPerSecond(value) {
  const formatted = bytes(value)
  return formatted === noData ? noData : `${formatted}/s`
}

function ratioHint(ratio, fallback) {
  return finite(ratio) ? `占 Step ${decimal(ratio * 100, 1)}%` : fallback
}

function cacheValue(summary) {
  const cacheBytes = bytes(summary.cacheBytes)
  const hits = summary.cacheHitsTotal
  const misses = summary.cacheMissesTotal
  if (!finite(hits) || !finite(misses) || hits + misses <= 0) return cacheBytes
  const hitRatio = `${decimal((hits / (hits + misses)) * 100, 1)}% 命中`
  return cacheBytes === noData ? hitRatio : `${cacheBytes} · ${hitRatio}`
}

const cards = computed(() => {
  const summary = props.summary || {}
  return [
    { label: 'Step 时间', value: seconds(summary.stepTimeSeconds), hint: '单步训练耗时' },
    { label: '数据等待', value: seconds(summary.dataTimeSeconds), hint: ratioHint(props.diagnosis?.ratios?.data, '等待训练上报') },
    { label: 'NCCL 通信', value: seconds(summary.ncclDurationSeconds), hint: ratioHint(props.diagnosis?.ratios?.nccl, '等待训练上报') },
    { label: 'Object Spill', value: bytesPerSecond(summary.objectStoreSpillBytesPerSecond), hint: `对象存储：${bytes(summary.objectStoreBytes)}` },
    { label: '缓存', value: cacheValue(summary), hint: '运行时缓存占用 / 命中率' },
  ]
})

const hasMissingData = computed(() => cards.value.some(({ value }) => value === noData))
</script>
