<template>
  <div class="space-y-5">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h4 class="text-sm font-bold text-white">{{ copy.title }}</h4>
        <p class="mt-1 text-xs text-slate-400">{{ copy.panelSummary }} 配额在每次提交时强制校验。</p>
      </div>
      <div v-if="isSuperAdmin" class="flex items-center gap-4">
        <el-switch :model-value="includeRetired" active-text="显示已退役团队" :disabled="busy || quotaVisible || retirementVisible" @change="toggleRetired" />
        <el-button type="primary" icon="Plus" class="!rounded-xl" :disabled="busy || quotaVisible || retirementVisible" @click="$emit('create-tenant')">新建租户 / 团队</el-button>
      </div>
    </div>

    <el-alert v-if="isSuperAdmin && copy.overAllocated" type="warning" show-icon :closable="false">
      <template #title>已分配配额超过当前训练提交容量</template>
      各租户配额之和为 {{ copy.allocatedGPUs }} 卡，当前训练提交容量为 {{ copy.capacityGPUs }} 卡；配额不是 GPU 预留，实际运行仍受调度和 Kueue 准入限制。
    </el-alert>
    <p v-if="isSuperAdmin" class="text-xs text-slate-400">已接入 GPU 不等于可用于训练的 GPU；训练提交容量还受节点就绪、可调度状态、训练池条件和平台提交上限限制。节点满足训练池条件后，平台自动同步 Kueue 容量；团队配额独立管理。容量未知时请刷新重试，不能据此判断扩容是否生效。</p>

    <section v-if="isSuperAdmin" class="panel p-4 space-y-2" aria-label="节点接入状态">
      <h5 class="text-sm font-bold text-white">节点接入状态</h5>
      <p class="text-xs text-slate-400">物理 GPU、节点就绪与存储验收分别显示；存储验收通过后仍需满足训练池与配额条件。未记录自动验收状态的节点按现有人工流程验收。</p>
      <p v-if="!nodeTopology.available" class="text-xs text-amber-300">{{ nodeTopology.error || '节点状态未知，请刷新重试。' }}</p>
      <el-table v-else :data="onboardingRows" empty-text="未发现物理 GPU 节点" size="small">
        <el-table-column prop="name" label="节点" min-width="150" />
        <el-table-column prop="gpus" label="物理 GPU" width="100" />
        <el-table-column prop="ready" label="节点就绪" width="100" />
        <el-table-column prop="scheduling" label="调度" width="120" />
        <el-table-column prop="stage" label="存储验收" min-width="190" />
        <el-table-column prop="reason" label="原因 / 进展" min-width="240" />
      </el-table>
    </section>

    <div class="grid gap-5 lg:grid-cols-3">
      <div
        v-for="tenant in displayedTenants"
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
          <el-tag size="small" :type="tenant.retiredAt ? 'info' : tenant.queuedJobsCount > 0 ? 'warning' : 'success'">
            {{ tenant.retiredAt ? '已退役' : tenant.queuedJobsCount > 0 ? `${tenant.queuedJobsCount} 任务排队中` : '配额正常' }}
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
          <div v-if="isSuperAdmin && !tenant.retiredAt" class="flex gap-3">
            <el-button v-if="isSuperAdmin && !tenant.retiredAt" type="primary" link size="small" :disabled="busy || retirementVisible" @click="openQuota(tenant)">修改配额</el-button>
            <el-button type="danger" link size="small" :disabled="busy || quotaVisible || retirementVisible" @click="openRetirement(tenant)">退役团队</el-button>
          </div>
          <span v-else-if="tenant.retiredAt">已退役 · 只读审计</span>
          <span v-else class="text-[11px] text-slate-600">仅超级管理员可调整</span>
        </div>
        <p v-if="tenant.retiredAt" class="text-xs text-slate-400">退役时间：{{ tenant.retiredAt }}<br>操作人：{{ tenant.retiredBy || '未记录' }}</p>
      </div>
    </div>

    <el-dialog v-model="quotaVisible" title="修改租户 GPU 配额" width="420px" :close-on-click-modal="!saving" :close-on-press-escape="!saving" :show-close="!saving" @closed="editing = null">
      <p class="mb-4 text-sm text-slate-400">
        为 <span class="font-mono text-slate-200">{{ editing?.name || editing?.id }}</span> 设置可同时占用的 GPU 上限。
        保存后立即对新提交生效；已在运行的任务不受影响。
      </p>
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="GPU 配额（卡）">
          <el-input-number v-model="quotaValue" :disabled="saving" :min="0" :max="4096" class="w-full" @keyup.enter="submitQuota" />
          <p class="mt-1 text-[11px] text-slate-500">当前已占用 {{ editing?.gpuQuotaUsed || 0 }} 卡；低于该值时新任务会被拒绝，运行中的任务不会被终止。</p>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button :disabled="saving" @click="quotaVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submitQuota">保存配额</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="retirementVisible" title="退役团队" width="560px" :close-on-click-modal="!busy" :close-on-press-escape="!busy" :show-close="!busy">
      <p class="mb-4 text-sm">团队：{{ retiring?.name || retiring?.id }}（{{ retiring?.id }}）</p>
      <el-alert type="warning" :closable="false" title="退役后保留历史、成员记录和存储，撤销令牌，禁止新提交，不释放空间。此页面不提供恢复操作。" />
      <p v-if="checking" class="mt-4">正在检查活动资源…</p>
      <dl v-if="preflight" class="my-4 grid grid-cols-2 gap-2 text-sm">
        <div v-for="row in retirementCountRows(preflight.counts)" :key="row.key" class="flex justify-between gap-2"><dt>{{ row.label }}</dt><dd>{{ row.value }}</dd></div>
      </dl>
      <ul v-if="preflight?.blockers?.length" class="my-4 list-disc pl-5 text-sm text-amber-400">
        <li v-for="(blocker, index) in preflight.blockers" :key="index">{{ retirementBlockerText(blocker) }}</li>
      </ul>
      <p v-if="preflight && !preflight.canRetire" class="my-3 text-sm text-amber-400">当前不能退役，请处理阻断项后重新检查。</p>
      <el-alert v-if="retirementError" class="my-4" type="error" :closable="false" :title="retirementError" />
      <el-form label-position="top" class="mt-4" @submit.prevent>
        <el-form-item :label="`输入团队 ID ${retiring?.id || ''} 确认退役`">
          <el-input v-model="confirmation" :disabled="busy" autocomplete="off" @keyup.enter="submitRetirement" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button :disabled="busy" @click="retirementVisible = false">取消</el-button>
        <el-button :disabled="busy" @click="checkRetirement">重新检查</el-button>
        <el-button type="danger" :loading="retirementSaving" :disabled="!canRetire" @click="submitRetirement">确认退役</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import { fetchTenantsForRetirementAudit, fetchTenantRetirementPreflight, retireTenant, setTenantGPUQuota } from '../../api/catalog'
import { adminQuotaModel, defaultPlatformLimits } from '../../platformLimits'
import { canConfirmRetirement, visibleTenants, retirementCountRows, retirementBlockerText } from '../../tenantRetirement'
import { nodeOnboardingRows } from '../../nodeOnboarding'

const props = defineProps({
  tenants: { type: Array, default: () => [] },
  isSuperAdmin: { type: Boolean, default: false },
  limits: { type: Object, default: () => defaultPlatformLimits },
  physicalGPUs: { type: Number, default: null },
  nodeTopology: { type: Object, default: () => ({ nodes: [], available: false, error: '' }) },
})
const onboardingRows = computed(() => nodeOnboardingRows(props.nodeTopology.nodes))
const emit = defineEmits(['create-tenant', 'changed'])

const quotaVisible = ref(false)
const editing = ref(null)
const quotaValue = ref(0)
const saving = ref(false)
const includeRetired = ref(false)
const auditTenants = ref([])
const auditLoading = ref(false)
const retiredIds = ref([])
const retirementVisible = ref(false)
const retiring = ref(null)
const preflight = ref(null)
const preflightReady = ref(false)
const confirmation = ref('')
const retirementError = ref('')
const checking = ref(false)
const retirementSaving = ref(false)
const busy = computed(() => saving.value || auditLoading.value || checking.value || retirementSaving.value)
const displayedTenants = computed(() => visibleTenants(
  includeRetired.value ? auditTenants.value : props.tenants.filter((tenant) => !retiredIds.value.includes(tenant.id)),
  includeRetired.value, props.isSuperAdmin,
))
const canRetire = computed(() => preflightReady.value && canConfirmRetirement({ isSuperAdmin: props.isSuperAdmin,
  tenant: retiring.value, preflight: preflight.value, confirmation: confirmation.value, busy: busy.value }))

watch(() => props.isSuperAdmin, (allowed) => {
  if (!allowed) { includeRetired.value = false; retirementVisible.value = false; quotaVisible.value = false }
})

const toggleRetired = async (value) => {
  if (!props.isSuperAdmin || busy.value) return
  if (!value) { includeRetired.value = false; return }
  auditLoading.value = true
  try {
    auditTenants.value = await fetchTenantsForRetirementAudit()
    includeRetired.value = props.isSuperAdmin
  } catch (error) { ElMessage.error(error.message || '加载退役团队失败') }
  finally { auditLoading.value = false }
}

const checkRetirement = async () => {
  if (!props.isSuperAdmin || !retiring.value || busy.value) return
  checking.value = true
  preflightReady.value = false
  retirementError.value = ''
  try {
    preflight.value = await fetchTenantRetirementPreflight(retiring.value.id)
    preflightReady.value = true
  }
  catch (error) { retirementError.value = error.message || '退役检查失败' }
  finally { checking.value = false }
}

const openRetirement = async (tenant) => {
  if (!props.isSuperAdmin || tenant.retiredAt || busy.value || quotaVisible.value) return
  retiring.value = tenant
  preflight.value = null
  confirmation.value = ''
  retirementError.value = ''
  retirementVisible.value = true
  await checkRetirement()
}

const submitRetirement = async () => {
  if (!canRetire.value) return
  retirementSaving.value = true
  retirementError.value = ''
  try {
    await retireTenant(retiring.value.id, confirmation.value)
    retiredIds.value = [...retiredIds.value, retiring.value.id]
    includeRetired.value = false
    retirementVisible.value = false
    ElMessage.success('团队已退役，历史与存储已保留')
    emit('changed')
  } catch (error) {
    preflightReady.value = false
    retirementError.value = `${error.message || '退役失败'}；请重新检查后重试`
  }
  finally { retirementSaving.value = false }
}

const copy = computed(() => adminQuotaModel({
  isSuperAdmin: props.isSuperAdmin,
  limits: props.limits,
  physicalGPUs: props.physicalGPUs,
  tenants: visibleTenants(props.tenants).filter((tenant) => !retiredIds.value.includes(tenant.id)),
}))

const usagePercentage = (tenant) => {
  const limit = Number(tenant.gpuQuotaLimit) || 0
  if (limit <= 0) return 0
  return Math.min(100, Math.round(((Number(tenant.gpuQuotaUsed) || 0) / limit) * 100))
}

const openQuota = (tenant) => {
  if (!props.isSuperAdmin || tenant.retiredAt || busy.value || retirementVisible.value) return
  editing.value = tenant
  quotaValue.value = Number(tenant.gpuQuotaLimit) || 0
  quotaVisible.value = true
}

const submitQuota = async () => {
  if (!props.isSuperAdmin || !editing.value || editing.value.retiredAt || busy.value) return
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
