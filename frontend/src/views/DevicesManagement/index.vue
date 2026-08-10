<template>
  <div class="space-y-6">
    <div class="flex justify-between items-center bg-[#131826] p-6 rounded-2xl border border-slate-800/80 shadow-xl">
      <div>
        <h3 class="text-lg font-bold text-white flex items-center gap-2">
          <el-icon class="text-blue-400"><Monitor /></el-icon> GPU 节点资源池
        </h3>
        <p class="text-xs text-slate-400 mt-1">资源数据来自 Kubernetes Node/Pod requests；GPU 利用率和温度需要额外部署 DCGM Exporter。</p>
      </div>
      <el-button icon="Refresh" class="!rounded-xl" @click="fetchTopology">刷新资源池</el-button>
    </div>

    <div v-if="topology" class="grid grid-cols-1 xl:grid-cols-3 gap-5">
      <div v-for="node in topology.nodes" :key="node.nodeName" class="bg-[#131826] p-6 rounded-2xl border border-slate-800/80 shadow-xl space-y-4">
        <div class="flex items-center justify-between">
          <span class="font-bold text-white font-mono">{{ node.nodeName }}</span>
          <el-tag type="success" size="small">Ready</el-tag>
        </div>
        <div class="grid grid-cols-2 gap-3 text-xs font-mono">
          <div class="bg-slate-950/70 rounded-lg p-3">
            <div class="text-slate-400">GPU 容量</div>
            <div class="text-blue-400 font-bold text-xl mt-1">{{ node.capacity }}</div>
          </div>
          <div class="bg-slate-950/70 rounded-lg p-3">
            <div class="text-slate-400">已分配</div>
            <div class="text-amber-400 font-bold text-xl mt-1">{{ node.allocated }}</div>
          </div>
        </div>
        <div class="space-y-1">
          <div class="flex justify-between text-xs text-slate-400">
            <span>调度占用</span><span>{{ node.capacity ? Math.round(node.allocated / node.capacity * 100) : 0 }}%</span>
          </div>
          <el-progress :percentage="node.capacity ? Math.round(node.allocated / node.capacity * 100) : 0" :show-text="false" />
        </div>
        <div class="text-xs text-slate-500">可调度: {{ node.available }} GPU · Allocatable: {{ node.allocatable }}</div>
      </div>
    </div>
    <el-empty v-else description="暂无 Kubernetes GPU 资源数据" />
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { apiGet } from '../../api/client'

const topology = ref(null)

const fetchTopology = async () => {
  try {
    topology.value = await apiGet('/api/v1/cluster/topology')
  } catch (error) {
    topology.value = null
    ElMessage.error(error.message || '无法读取 Kubernetes GPU 资源')
  }
}

onMounted(fetchTopology)
</script>
