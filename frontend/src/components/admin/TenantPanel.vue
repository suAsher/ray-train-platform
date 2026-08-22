<template>
  <div class="space-y-5">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h4 class="text-sm font-bold text-white">{{ copy.title }}</h4>
        <p class="mt-1 text-xs text-slate-400">{{ copy.panelSummary }} 配额在每次提交时强制校验。</p>
      </div>
      <el-button v-if="isSuperAdmin" type="primary" icon="Plus" class="!rounded-xl" @click="$emit('create-tenant')">新建租户 / 团队</el-button>
    </div>

    <el-alert v-if="isSuperAdmin && copy.allocatedGPUs > copy.capacityGPUs" type="warning" show-icon :closable="false">
      <template #title>已分配配额超过集群实际容量</template>
      各租户配额之和为 {{ copy.allocatedGPUs }} 卡，超过物理集群的 {{ copy.capacityGPUs }} 卡。超额部分会在 Kueue 准入阶段排队，不会真的获得 GPU。
    </el-alert>

    <div class="grid gap-5 lg:grid-cols-3">
      <div
        v-for="tenant in tenants"
        :key="tenant.id"
        class="panel panel-hover space-y-4 p-6"
      >
        <div class="flex items-start justify-between">
          <div>
            <h5 class="flex items-center gap-2 text-sm font-bold text-white">
              <el-icon class="text-blue-400"><UserFilled /></el-icon> {{ tenant.name || tenant.id }}
            </h5>
            <p class="mt-0.5 font-mono text-[11px] text-slate-400">Kueue 队列: {{ tenant.queueName }}</p>
          </div>
          <el-tag size="small" :type="tenant.queuedJobsCount > 0 ? 'warning' : 'success'">
            {{ tenant.queuedJobsCount > 0 ? `${tenant.queuedJobsCount} 任务排队中` : '配额正常' }}
          </el-tag>
        </div>

        <div class="space-y-1.5">
          <div class="flex justify-between font-mono text-xs">
            <span class="text-slate-400">管理员分配额度</span>
            <span class="font-bold text-blue-400">{{ tenant.gpuQuotaLimit }} 卡</span>
          </div>
          <div class="flex justify-between font-mono text-xs">
            <span class="text-slate-400">已使用</span>
            <span class="font-bold text-amber-300">{{ tenant.gpuQuotaUsed }} 卡</span>
          </div>
          <el-progress
            :percentage="usagePercentage(tenant)"
            :status="tenant.gpuQuotaUsed >= tenant.gpuQuotaLimit ? 'warning' : 'success'"
            :show-text="false"
          />
        </div>

        <div class="flex items-center justify-between border-t border-slate-800/60 pt-2 font-mono text-xs text-slate-400">
          <span>运行中 {{ tenant.activeJobsCount || 0 }} 个任务</span>
          <el-button v-if="isSuperAdmin" type="primary" link size="small" @click="openQuota(tenant)">修改配额</el-button>
          <span v-else class="text-[11px] text-slate-600">仅超级管理员可调整</span>
        </div>
      </div>
    </div>

    <el-dialog v-model="quotaVisible" title="修改租户 GPU 配额" width="420px" @closed="editing = null">
      <p class="mb-4 text-sm text-slate-400">
        为 <span class="font-mono text-slate-200">{{ editing?.name || editing?.id }}</span> 设置可同时占用的 GPU 上限。
        保存后立即对新提交生效；已在运行的任务不受影响。
      </p>
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="GPU 配额（卡）">
          <el-input-number v-model="quotaValue" :min="0" :max="4096" class="w-full" @keyup.enter="submitQuota" />
          <p class="mt-1 text-[11px] text-slate-500">当前已占用 {{ editing?.gpuQuotaUsed || 0 }} 卡；低于该值时新任务会被拒绝，运行中的任务不会被终止。</p>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="quotaVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submitQuota">保存配额</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'

import { setTenantGPUQuota } from '../../api/catalog'
import { adminQuotaModel, defaultPlatformLimits } from '../../platformLimits'

const props = defineProps({
  tenants: { type: Array, default: () => [] },
  isSuperAdmin: { type: Boolean, default: false },
  limits: { type: Object, default: () => defaultPlatformLimits },
})
const emit = defineEmits(['create-tenant', 'changed'])

const quotaVisible = ref(false)
const editing = ref(null)
const quotaValue = ref(0)
const saving = ref(false)

const copy = computed(() => adminQuotaModel({
  isSuperAdmin: props.isSuperAdmin,
  limits: props.limits,
  tenants: props.tenants,
}))

const usagePercentage = (tenant) => {
  const limit = Number(tenant.gpuQuotaLimit) || 0
  if (limit <= 0) return 0
  return Math.min(100, Math.round(((Number(tenant.gpuQuotaUsed) || 0) / limit) * 100))
}

const openQuota = (tenant) => {
  editing.value = tenant
  quotaValue.value = Number(tenant.gpuQuotaLimit) || 0
  quotaVisible.value = true
}

const submitQuota = async () => {
  if (!editing.value) return
  saving.value = true
  try {
    await setTenantGPUQuota(editing.value.id, quotaValue.value)
    ElMessage.success(`已将 ${editing.value.name || editing.value.id} 的 GPU 配额调整为 ${quotaValue.value} 卡`)
    quotaVisible.value = false
    emit('changed')
  } catch (error) {
    ElMessage.error(error.message || '保存配额失败')
  } finally {
    saving.value = false
  }
}
</script>
