<template>
  <el-table :data="allocations" class="!bg-transparent text-xs" empty-text="当前没有 GPU 占用记录">
    <el-table-column label="类型" width="120">
      <template #default="scope">
        <el-tag size="small" effect="dark" :type="allocationTypeTag(scope.row.type)">
          {{ allocationTypeLabel(scope.row.type) }}
        </el-tag>
      </template>
    </el-table-column>
    <el-table-column prop="name" label="名称" min-width="190">
      <template #default="scope">
        <div class="font-mono font-bold text-slate-200">{{ scope.row.name }}</div>
        <div class="mt-0.5 font-mono text-[11px] text-slate-500">{{ scope.row.id }}</div>
      </template>
    </el-table-column>
    <el-table-column prop="username" label="用户" min-width="130">
      <template #default="scope">
        <span class="font-semibold text-white">{{ scope.row.username }}</span>
      </template>
    </el-table-column>
    <el-table-column prop="tenantId" label="团队" min-width="120" />
    <el-table-column label="状态" width="110">
      <template #default="scope">
        <el-tag size="small" :type="allocationStateTag(scope.row.state)">{{ scope.row.state }}</el-tag>
      </template>
    </el-table-column>
    <el-table-column label="GPU" width="80">
      <template #default="scope"><span class="font-mono font-bold text-blue-400">{{ scope.row.gpuCount }} 卡</span></template>
    </el-table-column>
    <el-table-column label="启动时间" width="180">
      <template #default="scope">{{ formatAllocationTime(scope.row.startedAt || scope.row.createdAt) }}</template>
    </el-table-column>
    <el-table-column label="运行时长" width="120">
      <template #default="scope">{{ formatAllocationDuration(scope.row) }}</template>
    </el-table-column>
    <el-table-column label="资源位置" min-width="220">
      <template #default="scope">
        <div class="font-mono text-[11px] text-slate-300">{{ scope.row.namespace || '—' }}</div>
        <div class="mt-0.5 font-mono text-[11px] text-slate-500">{{ scope.row.resourceName || '—' }}</div>
      </template>
    </el-table-column>
  </el-table>
</template>

<script setup>
import {
  allocationStateTag,
  allocationTypeLabel,
  allocationTypeTag,
  formatAllocationDuration,
  formatAllocationTime,
} from '../../gpuAllocations'

defineProps({ allocations: { type: Array, default: () => [] } })
</script>
