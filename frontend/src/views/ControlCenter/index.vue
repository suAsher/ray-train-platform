<template>
  <div class="space-y-6">
    <div class="grid grid-cols-4 gap-5">
      <el-card v-for="card in cards" :key="card.label" shadow="never" class="!bg-[#131826] !border-slate-800/80 !rounded-2xl">
        <p class="text-xs text-slate-400">{{ card.label }}</p>
        <p class="text-2xl font-bold text-white font-mono mt-2">{{ card.value }}</p>
      </el-card>
    </div>
    <div class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 space-y-4">
      <div class="flex items-center justify-between">
        <div>
          <h3 class="text-sm font-bold text-slate-200">运行中的 Ray 训练任务</h3>
          <p class="text-xs text-slate-500 mt-1">超级管理员查看全平台任务；普通用户仅查看本团队。任务结束后的 RayCluster 在清理窗口内仍可能计入物理已分配 GPU。</p>
        </div>
        <el-button icon="Refresh" @click="refresh">刷新</el-button>
      </div>
      <el-table :data="runningJobs" style="width: 100%" class="!bg-transparent text-xs">
        <el-table-column prop="name" label="任务名称" />
        <el-table-column prop="observedState" label="状态" />
        <el-table-column label="GPU 申请">
          <template #default="scope">{{ gpuCount(scope.row) }}</template>
        </el-table-column>
        <el-table-column prop="createdAt" label="提交时间" />
      </el-table>
      <el-empty v-if="!runningJobs.length" description="当前没有运行中的任务" />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { apiGet } from '../../api/client'

const topology = ref({ totalNodes: 0, totalGpus: 0, usedGpus: 0 })
const runningJobs = ref([])
const cards = computed(() => [
  { label: 'GPU 卡总数', value: `${topology.value.totalGpus} 卡` },
  { label: 'GPU 节点数', value: `${topology.value.totalNodes} 节点` },
  { label: '已分配 GPU', value: `${topology.value.usedGpus} 卡` },
  { label: '运行中任务', value: `${runningJobs.value.length} 个` }
])

const gpuCount = (job) => (job.spec?.resources?.workerReplicas || 0) * (job.spec?.resources?.gpusPerWorker || 0)

const refresh = async () => {
  try {
    topology.value = await apiGet('/api/v1/cluster/topology')
    const page = await apiGet('/api/v1/jobs?status=RUNNING')
    runningJobs.value = page.items || []
  } catch (error) {
    ElMessage.error(error.message || '无法读取集群状态')
    topology.value = { totalNodes: 0, totalGpus: 0, usedGpus: 0 }
    runningJobs.value = []
  }
}

onMounted(refresh)
</script>
