<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h4 class="flex items-center gap-2 text-sm font-bold text-white">
          <el-icon class="text-amber-400"><Clock /></el-icon> 队列与运行中的任务
        </h4>
        <p class="mt-1 text-xs text-slate-400">
          集群满载或租户超配额时，新任务由 Kueue 组调度排队。这里只能取消或停止当前租户的任务，其他租户任务为只读。
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

import { queueJobAction } from './queuePanelActions.js'

const props = defineProps({
  jobs: { type: Array, default: () => [] },
  clusterGPUs: { type: Number, default: 0 },
  currentTenantId: { type: String, default: '' },
})

defineEmits(['cancel-job'])

const actionFor = (job) => queueJobAction(job, props.currentTenantId)

const cards = computed(() => {
  const running = props.jobs.filter((job) => job.state === 'RUNNING')
  const queued = props.jobs.filter((job) => job.state !== 'RUNNING')
  const busyGPUs = running.reduce((total, job) => total + (job.gpus || 0), 0)
  return [
    { label: '运行中', value: running.length, tone: 'text-blue-400' },
    { label: '排队中', value: queued.length, tone: 'text-amber-400' },
    { label: '占用 GPU', value: `${busyGPUs} / ${props.clusterGPUs}`, tone: 'text-emerald-400' },
    { label: '排队等待 GPU', value: queued.reduce((total, job) => total + (job.gpus || 0), 0), tone: 'text-slate-100' },
  ]
})
</script>
