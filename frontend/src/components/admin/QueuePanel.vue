<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h4 class="flex items-center gap-2 text-sm font-bold text-white">
          <el-icon class="text-amber-400"><Clock /></el-icon> 队列与运行中的任务
        </h4>
        <p class="mt-1 text-xs text-slate-400">
          集群满载或团队超配额时，新任务由 Kueue 组调度排队。超级管理员可查看并停止任意团队任务；团队管理员仅管理本团队。
        </p>
      </div>
      <el-tag type="warning" size="small">Kueue Gang Scheduling 生效中</el-tag>
    </div>

    <div class="grid grid-cols-4 gap-4">
      <div v-for="card in cards" :key="card.label" class="stat-tile">
        <p class="stat-tile__label">{{ card.label }}</p>
        <p class="stat-tile__value" :class="card.tone">{{ card.value }}</p>
      </div>
    </div>

    <el-table :data="jobs" class="!bg-transparent text-xs" empty-text="当前没有排队或运行中的任务">
      <el-table-column prop="name" label="任务名称" min-width="220">
        <template #default="scope">
          <span class="font-mono font-bold text-slate-200">{{ scope.row.name }}</span>
          <div class="mt-0.5 font-mono text-[11px] text-slate-500">{{ scope.row.id }}</div>
        </template>
      </el-table-column>
      <el-table-column prop="tenantId" label="所属租户" width="160" />
      <el-table-column prop="state" label="状态" width="120">
        <template #default="scope">
          <el-tag size="small" :type="scope.row.state === 'RUNNING' ? 'primary' : 'warning'" effect="dark">{{ scope.row.state }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="gpus" label="GPU" width="120">
        <template #default="scope"><span class="font-mono font-bold text-blue-400">{{ scope.row.gpus }} 卡</span></template>
      </el-table-column>
      <el-table-column prop="createdAt" label="提交时间" width="200" />
      <el-table-column label="管理员操作" width="140" align="right">
        <template #default="scope">
          <el-button
            v-if="actionFor(scope.row)"
            :type="actionFor(scope.row).kind === 'cancel-queue' ? 'warning' : 'danger'"
            link
            size="small"
            @click="$emit('cancel-job', scope.row)"
          >
            {{ actionFor(scope.row).label }}
          </el-button>
          <span v-else class="text-[11px] text-slate-600">只读</span>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup>
import { computed } from 'vue'

import { queueJobAction, queuePanelStats } from './queuePanelActions.js'

const props = defineProps({
  jobs: { type: Array, default: () => [] },
  clusterGPUs: { type: Number, default: 0 },
  physicalAllocatedGPUs: { type: Number, default: 0 },
  currentTenantId: { type: String, default: '' },
  isSuperAdmin: { type: Boolean, default: false },
})

defineEmits(['cancel-job'])

const actionFor = (job) => queueJobAction(job, props.currentTenantId, props.isSuperAdmin)

const cards = computed(() => {
  const stats = queuePanelStats(props.jobs, props.clusterGPUs, props.physicalAllocatedGPUs)
  return [
    { label: '运行中任务', value: stats.runningJobs, tone: 'text-blue-400' },
    { label: '排队 / 准备', value: stats.waitingJobs, tone: 'text-amber-400' },
    { label: '任务申请 GPU', value: `${stats.activeRequestedGPUs} / ${stats.clusterGPUs}`, tone: 'text-emerald-400' },
    { label: stats.releasingGPUs > 0 ? '物理已分配（含释放中）' : '物理已分配 GPU', value: stats.physicalAllocatedGPUs, tone: 'text-slate-100' },
  ]
})
</script>
