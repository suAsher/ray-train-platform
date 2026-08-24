<template>
  <div class="space-y-6">
    <div class="grid grid-cols-1 gap-5 md:grid-cols-3 xl:grid-cols-6">
      <el-card v-for="card in cards" :key="card.label" shadow="never" class="!bg-[#131826] !border-slate-800/80 !rounded-2xl">
        <p class="text-xs text-slate-400">{{ card.label }}</p>
        <p class="text-2xl font-bold text-white font-mono mt-2">{{ card.value }}</p>
      </el-card>
    </div>
    <div class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 class="text-sm font-bold text-slate-200">GPU 占用明细</h3>
          <p class="text-xs text-slate-500 mt-1">包含训练任务和交互式调试环境。超级管理员查看全平台，团队管理员仅查看本团队。</p>
          <p v-if="allocationsLoaded && allocationStats.detailedGPUs !== topology.usedGpus" class="text-xs text-amber-300 mt-1">
            明细合计 {{ allocationStats.detailedGPUs }} 卡，物理已分配 {{ topology.usedGpus }} 卡；差值通常来自 RayCluster 创建或释放窗口。
          </p>
        </div>
        <el-button icon="Refresh" @click="refresh">刷新</el-button>
      </div>
      <el-alert v-if="allocationError" type="error" :closable="false" show-icon>
        <template #title>GPU 占用明细暂不可用{{ allocationsLoaded ? '，当前显示上次成功结果' : '' }}</template>
        {{ allocationError }}
      </el-alert>
      <GPUAllocationTable v-if="allocationsLoaded" :allocations="allocations" />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { apiGet } from '../../api/client'
import GPUAllocationTable from '../../components/admin/GPUAllocationTable.vue'
import { allocationSummary, normalizeGPUAllocations } from '../../gpuAllocations'

const topology = ref({ totalNodes: 0, totalGpus: 0, usedGpus: 0 })
const allocations = ref([])
const allocationsLoaded = ref(false)
const allocationError = ref('')
const allocationStats = computed(() => allocationSummary(allocations.value))
const cards = computed(() => [
  { label: 'GPU 卡总数', value: `${topology.value.totalGpus} 卡` },
  { label: 'GPU 节点数', value: `${topology.value.totalNodes} 节点` },
  { label: '物理已分配 GPU', value: `${topology.value.usedGpus} 卡` },
  { label: '明细占用 GPU', value: allocationsLoaded.value ? `${allocationStats.value.detailedGPUs} 卡` : '暂不可用' },
  { label: '训练任务', value: allocationsLoaded.value ? `${allocationStats.value.trainingJobs} 个` : '暂不可用' },
  { label: '调试环境', value: allocationsLoaded.value ? `${allocationStats.value.debugWorkspaces} 个` : '暂不可用' }
])

const refresh = async () => {
  const [topologyResult, allocationResult] = await Promise.allSettled([
    apiGet('/api/v1/cluster/topology'),
    apiGet('/api/v1/gpu-allocations'),
  ])
  if (topologyResult.status === 'fulfilled') {
    topology.value = topologyResult.value
  } else {
    ElMessage.error(topologyResult.reason?.message || '无法读取集群物理状态')
  }
  if (allocationResult.status === 'fulfilled') {
    allocations.value = normalizeGPUAllocations(allocationResult.value)
    allocationsLoaded.value = true
    allocationError.value = ''
  } else {
    allocationError.value = allocationResult.reason?.message || '无法读取 GPU 占用明细'
    ElMessage.error(allocationError.value)
  }
}

onMounted(refresh)
</script>
