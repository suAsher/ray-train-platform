<template>
  <section class="space-y-5" aria-labelledby="data-performance-title">
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

    <section
      v-for="group in groups"
      :key="group.id"
      class="space-y-2"
      :aria-labelledby="group.id"
    >
      <div>
        <h6 :id="group.id" class="text-xs font-semibold text-slate-300">{{ group.title }}</h6>
        <p class="mt-0.5 text-[11px] text-slate-500">{{ group.description }}</p>
      </div>
      <dl class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div
          v-for="card in group.cards"
          :key="card.label"
          class="rounded-xl border border-slate-800/80 bg-slate-900/55 p-4"
        >
          <dt class="text-xs text-slate-400">{{ card.label }}</dt>
          <dd class="mt-1 font-mono text-lg font-semibold tabular-nums text-slate-100">{{ card.value }}</dd>
          <p class="mt-1 text-[11px] leading-relaxed text-slate-500">{{ card.hint }}</p>
        </div>
      </dl>
    </section>

    <p v-if="hasMissingData" class="text-xs text-slate-500">“暂无数据”表示指标未上报或当前时间窗口没有样本，不会按 0 处理。历史任务可继续查看已有指标。</p>
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
const firstFinite = (...values) => values.find(finite) ?? null
const decimal = (value, digits = 2) => value.toLocaleString('zh-CN', { maximumFractionDigits: digits })
const seconds = (value) => finite(value) ? `${decimal(value, 3)} s` : noData
const count = (value) => finite(value) ? decimal(value, 0) : noData
const perSecond = (value, unit) => finite(value) ? `${decimal(value, 2)} ${unit}/s` : noData
const percent = (value) => finite(value) ? `${decimal(value * 100, 1)}%` : noData

function bytes(value) {
  if (!finite(value)) return noData
  if (value >= 1024 ** 4) return `${decimal(value / 1024 ** 4)} TiB`
  if (value >= 1024 ** 3) return `${decimal(value / 1024 ** 3)} GiB`
  if (value >= 1024 ** 2) return `${decimal(value / 1024 ** 2)} MiB`
  if (value >= 1024) return `${decimal(value / 1024)} KiB`
  return `${decimal(value, 0)} B`
}

function bytesPerSecond(value) {
  const formatted = bytes(value)
  return formatted === noData ? noData : `${formatted}/s`
}

function ratioHint(ratio, fallback, subject = 'Step') {
  return finite(ratio) ? `占 ${subject} ${decimal(ratio * 100, 1)}%` : fallback
}

function ratioFromCounts(hits, misses) {
  if (!finite(hits) || !finite(misses) || hits + misses <= 0) return null
  return hits / (hits + misses)
}

function sourceSamplesHint(summary) {
  const estimate = '按实际读取 Parquet Row Group 的压缩字节估算，不代表物理网络流量'
  return finite(summary.datasetSourceSamplesPerSecond)
    ? `样本吞吐 ${decimal(summary.datasetSourceSamplesPerSecond, 2)} samples/s · ${estimate}`
    : estimate
}

function cumulativeSecondsHint(value, fallback) {
  return finite(value) ? `任务累计 ${seconds(value)}` : fallback
}

function cacheCounters(summary) {
  const datasetHits = summary.datasetCacheHitsTotal
  const datasetMisses = summary.datasetCacheMissesTotal
  if (finite(datasetHits) && finite(datasetMisses)) {
    return { hits: datasetHits, misses: datasetMisses }
  }
  return { hits: summary.cacheHitsTotal, misses: summary.cacheMissesTotal }
}

function cacheHitRatio(summary) {
  const counters = cacheCounters(summary)
  return firstFinite(summary.datasetCacheHitRatio, ratioFromCounts(counters.hits, counters.misses))
}

function cacheHitHint(summary) {
  const counters = cacheCounters(summary)
  if (!finite(counters.hits) || !finite(counters.misses)) return '等待缓存访问计数上报'
  return `命中 ${count(counters.hits)} · 未命中 ${count(counters.misses)}`
}

function stallValue(stall) {
  if (stall?.status === 'detected') return '检测到'
  if (stall?.status === 'clear') return '未检测到'
  return noData
}

const groups = computed(() => {
  const summary = props.summary || {}
  const diagnosis = props.diagnosis || {}
  const stall = diagnosis.dataStall || {}
  return [
    {
      id: 'training-loop-performance',
      title: '训练主循环',
      description: '区分计算、数据准备和跨节点通信耗时。',
      cards: [
        { label: 'Step 时间', value: seconds(summary.stepTimeSeconds), hint: '单步训练耗时' },
        { label: '数据等待', value: seconds(summary.dataTimeSeconds), hint: ratioHint(diagnosis.ratios?.data, '等待训练上报') },
        { label: 'NCCL 通信', value: seconds(summary.ncclDurationSeconds), hint: ratioHint(diagnosis.ratios?.nccl, '等待训练上报') },
      ],
    },
    {
      id: 'source-read-performance',
      title: '源数据读取',
      description: '反映流式数据从源存储进入 Ray Data 的速度和尾延迟。',
      cards: [
        { label: '源读取吞吐', value: bytesPerSecond(summary.datasetSourceBytesPerSecond), hint: sourceSamplesHint(summary) },
        { label: '源分片速率', value: perSecond(summary.datasetSourceShardsPerSecond, 'shards'), hint: 'Parquet 分片读取调用，不代表原始小文件数' },
        { label: '源读取 P95', value: seconds(summary.datasetSourceReadP95Seconds), hint: '95% 的源读取耗时不超过此值' },
      ],
    },
    {
      id: 'ray-data-pipeline-performance',
      title: 'Ray Data 流水线',
      description: '观察预取供给、算子背压，以及 GPU 是否在等待数据。',
      cards: [
        {
          label: 'Ray 预取等待',
          value: percent(summary.datasetPrefetchWaitRatio),
          hint: finite(summary.datasetPrefetchWaitP95Seconds)
            ? `单批等待 P95 ${seconds(summary.datasetPrefetchWaitP95Seconds)} · ${cumulativeSecondsHint(summary.datasetPrefetchWaitSecondsTotal, '等待累计值上报')}`
            : cumulativeSecondsHint(summary.datasetPrefetchWaitSecondsTotal, '每个 Worker 的窗口平均等待占比'),
        },
        {
          label: 'Ray 背压',
          value: percent(summary.datasetBackpressureRatio),
          hint: cumulativeSecondsHint(summary.datasetBackpressureSecondsTotal, '下游消费变慢时会升高'),
        },
        { label: 'GPU 数据停顿', value: stallValue(stall), hint: stall.reason || '等待 GPU 与数据等待指标上报' },
      ],
    },
    {
      id: 'nvme-cache-performance',
      title: 'NVMe 有界缓存',
      description: '展示当前任务的本地分片工作集；旧任务会回退显示原缓存指标。',
      cards: [
        { label: '缓存命中率', value: percent(cacheHitRatio(summary)), hint: cacheHitHint(summary) },
        {
          label: '缓存读取吞吐',
          value: bytesPerSecond(summary.datasetCacheBytesPerSecond),
          hint: finite(summary.datasetCacheBytesReadTotal)
            ? `Row Group 压缩字节估算 ${bytes(summary.datasetCacheBytesReadTotal)}${finite(summary.datasetCacheReadP95Seconds) ? ` · 读取 P95 ${seconds(summary.datasetCacheReadP95Seconds)}` : ''}`
            : finite(summary.datasetCacheReadP95Seconds)
              ? `读取 P95 ${seconds(summary.datasetCacheReadP95Seconds)}`
              : '等待 NVMe 分片读取字节数上报',
        },
        {
          label: 'NVMe 有界缓存',
          value: bytes(firstFinite(summary.datasetCacheBytesTotal, summary.cacheBytes)),
          hint: finite(summary.datasetCacheFallbacksTotal)
            ? `回退源读取 ${count(summary.datasetCacheFallbacksTotal)} 次`
            : finite(summary.datasetCacheBytesTotal)
              ? '任务累计写入本地分片'
              : '旧任务缓存占用',
        },
        {
          label: '缓存淘汰',
          value: count(summary.datasetCacheEvictionsTotal),
          hint: finite(summary.datasetCacheStaleTempReclaimedTotal)
            ? `回收临时文件 ${count(summary.datasetCacheStaleTempReclaimedTotal)} 个`
            : '达到高水位后的 LRU 回收次数',
        },
        {
          label: '校验失败',
          value: count(summary.datasetCacheChecksumFailuresTotal),
          hint: finite(summary.datasetCacheDownloadsTotal)
            ? `下载 ${count(summary.datasetCacheDownloadsTotal)} 次`
            : '摘要校验失败会回退源读取',
        },
      ],
    },
    {
      id: 'object-spill-performance',
      title: 'Object Spill',
      description: 'Ray 对象存储内存不足时的本地溢写，不等同于训练数据缓存。',
      cards: [
        { label: 'Spill 吞吐', value: bytesPerSecond(summary.objectStoreSpillBytesPerSecond), hint: 'Ray 对象溢写到本地盘的速率' },
        { label: '窗口内溢写', value: bytes(summary.objectStoreSpillBytes), hint: '根据当前时间窗口的 Spill 曲线积分' },
        { label: '对象存储占用', value: bytes(summary.objectStoreBytes), hint: 'Ray Object Store 当前占用' },
      ],
    },
  ]
})

const hasMissingData = computed(() => groups.value.some((group) => group.cards.some(({ value }) => value === noData)))
</script>
