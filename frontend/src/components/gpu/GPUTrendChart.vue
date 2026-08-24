<template>
  <section class="rounded-2xl border border-slate-800/90 bg-slate-950/55 p-4">
    <div class="mb-3 flex items-center justify-between gap-3">
      <div>
        <h4 class="text-sm font-semibold text-slate-200">{{ title }}</h4>
        <p class="mt-0.5 text-[11px] text-slate-500">{{ description }}</p>
      </div>
      <span class="rounded-lg bg-slate-900 px-2 py-1 font-mono text-[10px] text-slate-400">{{ unit }}</span>
    </div>
    <div v-if="series.length" ref="chartElement" class="h-64 w-full" />
    <div v-else class="flex h-64 items-center justify-center text-xs text-slate-500">当前范围没有指标样本</div>
  </section>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

const props = defineProps({
  title: { type: String, required: true },
  description: { type: String, default: '' },
  unit: { type: String, default: '' },
  series: { type: Array, default: () => [] },
  minimum: { type: Number, default: undefined },
  maximum: { type: Number, default: undefined },
  scale: { type: Number, default: 1 },
})

const chartElement = ref(null)
let chart
let resizeObserver
const palette = ['#38bdf8', '#34d399', '#fbbf24', '#fb7185', '#a78bfa', '#22d3ee', '#f97316', '#84cc16']

const options = computed(() => ({
  animationDuration: 250,
  color: palette,
  grid: { left: 50, right: 18, top: 42, bottom: 34 },
  legend: {
    type: 'scroll', top: 0, right: 0,
    textStyle: { color: '#94a3b8', fontSize: 10 },
    pageTextStyle: { color: '#64748b' },
  },
  tooltip: {
    trigger: 'axis',
    backgroundColor: 'rgba(2, 6, 23, .96)',
    borderColor: '#334155',
    textStyle: { color: '#e2e8f0', fontSize: 11 },
    valueFormatter: (value) => `${Number(value).toFixed(props.scale === 1 ? 0 : 1)} ${props.unit}`,
  },
  xAxis: {
    type: 'time',
    axisLine: { lineStyle: { color: '#334155' } },
    axisLabel: { color: '#64748b', fontSize: 10 },
    splitLine: { show: false },
  },
  yAxis: {
    type: 'value', min: props.minimum, max: props.maximum,
    axisLabel: { color: '#64748b', fontSize: 10 },
    splitLine: { lineStyle: { color: 'rgba(51, 65, 85, .45)' } },
  },
  series: props.series.map((item) => ({
    id: item.id,
    name: item.name,
    type: 'line',
    showSymbol: false,
    smooth: 0.18,
    connectNulls: false,
    lineStyle: { width: 1.8 },
    emphasis: { focus: 'series', lineStyle: { width: 3 } },
    data: item.data.map(([timestamp, value]) => [timestamp, value / props.scale]),
  })),
}))

async function render() {
  await nextTick()
  if (!chartElement.value) {
    chart?.dispose()
    chart = undefined
    return
  }
  if (chart && chart.getDom() !== chartElement.value) {
    chart.dispose()
    chart = undefined
  }
  if (!chart) chart = echarts.init(chartElement.value, null, { renderer: 'canvas' })
  resizeObserver?.disconnect()
  resizeObserver?.observe(chartElement.value)
  chart.setOption(options.value, { notMerge: true })
}

watch(options, render, { deep: true })
watch(() => props.series.length, render)

onMounted(() => {
  resizeObserver = new ResizeObserver(() => chart?.resize())
  render()
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  chart?.dispose()
  chart = undefined
})
</script>
