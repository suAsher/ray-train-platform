<template>
  <section class="space-y-3" aria-labelledby="recovery-timeline-title">
    <div>
      <h5 id="recovery-timeline-title" class="text-sm font-semibold text-slate-100">恢复时间线</h5>
      <p class="mt-1 text-xs text-slate-500">展示受管训练的集群尝试、检查点续训和 Worker 重启记录。</p>
    </div>

    <ol v-if="points.length" class="space-y-3 border-l border-slate-700 pl-5">
      <li v-for="(point, index) in points" :key="`${point.at || 'recovery'}:${index}`" class="relative rounded-xl border border-slate-800/80 bg-slate-900/45 p-4">
        <span class="absolute -left-[1.55rem] top-5 h-2.5 w-2.5 rounded-full bg-blue-400 ring-4 ring-slate-900" aria-hidden="true"></span>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="font-semibold text-slate-100">集群尝试 {{ displayInteger(point.clusterAttempt) }}</span>
          <time :datetime="point.at || undefined" class="font-mono text-xs text-slate-400">{{ formatTime(point.at) }}</time>
        </div>
        <dl class="mt-3 grid gap-3 text-xs sm:grid-cols-2">
          <div><dt class="text-slate-500">恢复检查点</dt><dd class="mt-1 break-all font-mono text-slate-200">{{ checkpointLabel(point) }}</dd></div>
          <div><dt class="text-slate-500">重启次数</dt><dd class="mt-1 font-mono text-slate-200">{{ displayInteger(point.restartCount) }}</dd></div>
        </dl>
      </li>
    </ol>
    <div v-else class="rounded-xl border border-dashed border-slate-800 px-4 py-8 text-center text-sm text-slate-500">暂无恢复记录</div>
  </section>
</template>

<script setup>
defineProps({
  points: { type: Array, default: () => [] },
})

const noData = '暂无数据'
const finite = (value) => typeof value === 'number' && Number.isFinite(value)
const displayInteger = (value) => finite(value) ? Math.trunc(value).toLocaleString('zh-CN') : noData

function formatTime(value) {
  const date = new Date(value || '')
  return Number.isFinite(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false }) : noData
}

function checkpointLabel(point) {
  if (!point.resumeCheckpointId) return '未从检查点恢复'
  return point.checkpointStep === null || point.checkpointStep === undefined
    ? point.resumeCheckpointId
    : `${point.resumeCheckpointId}（Step ${displayInteger(point.checkpointStep)}）`
}
</script>
