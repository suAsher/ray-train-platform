<template>
  <div class="mx-auto max-w-6xl space-y-6">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <p class="text-xs font-semibold uppercase tracking-[0.18em] text-blue-400">Ray Training</p>
        <h1 class="mt-1 text-2xl font-bold text-white">新建训练任务</h1>
        <p class="mt-2 text-sm text-slate-400">依次选择代码、算力和数据。平台会将你的选择固化为可复现的训练任务。</p>
      </div>
      <router-link to="/job"><el-button>返回任务列表</el-button></router-link>
    </div>

    <el-alert v-if="fromWorkspaceSnapshot" type="success" :closable="false" class="!rounded-2xl">
      <template #title>已从调试工作区带入代码快照</template>
      训练任务会从不可变快照物化代码；请继续补全运行规模和数据位置。
    </el-alert>

    <el-alert v-if="resumeCheckpointPath" type="warning" :closable="false" class="!rounded-2xl">
      <template #title>将从已有训练结果继续训练</template>
      已带入只读 Checkpoint：<code>我的训练结果/{{ resumeCheckpointPath }}</code>。这是一个新任务，不会修改原任务；
      训练脚本需要支持从 <code>$PLATFORM_CHECKPOINT_PATH</code> 恢复（例如 <code>--resume-from</code> 或 <code>--auto-resume</code>）。
    </el-alert>

    <el-alert v-if="quotaModel.blocked" type="error" :closable="false" show-icon class="!rounded-2xl" :title="quotaModel.blockMessage" />

    <div class="panel p-3">
      <el-steps :active="currentStep" finish-status="success" simple>
        <el-step title="代码与环境" description="选择可复现的运行基础" />
        <el-step title="运行规模" description="选择训练方式与启动命令" />
        <el-step title="数据与确认" description="声明输入和训练产物" />
      </el-steps>
    </div>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_280px]">
      <section class="panel p-6">
        <StepCode
          v-if="currentStep === 0"
          :form="form"
          :images="trainingImages"
          :snapshots="workspaceSnapshots"
          :loading="loadingCatalog"
          :workspace-path="mountPaths.workspace"
        />
        <div v-else-if="currentStep === 1">
          <div class="mb-6">
            <h2 class="text-lg font-semibold text-white">运行规模</h2>
            <p class="mt-1 text-sm text-slate-400">
              选择训练方式，平台会据此决定命令在哪里、以什么并行方式运行。{{ quotaModel.scopeLabel }}：
              {{ quotaModel.maxWorkerReplicas }} 个节点 × {{ quotaModel.maxGpusPerWorker }} 卡，单任务最多
              {{ quotaModel.maxTotalGpus }} 卡。
            </p>
          </div>
          <StepRuntime
            class="role-aware-runtime-step"
            :form="form"
            :profiles="profiles"
            :limits="formLimits"
            :execution-mode="executionMode"
            :command-preview="commandPreview"
            :warnings="commandWarnings"
            :workspace-path="mountPaths.workspace"
            @apply-profile="applyProfile"
          />
        </div>
        <template v-else>
          <StepData :form="form" :mount-paths="mountPaths" />
          <SubmitPreview
            class="mt-6"
            :form="form"
            :issues="allIssues"
            :total-g-p-us="totalGPUs"
            :execution-mode="executionMode"
            :command-preview="commandPreview"
          />
        </template>

        <div class="mt-8 flex items-center justify-between border-t border-slate-800 pt-5">
          <el-button :disabled="currentStep === 0" @click="currentStep -= 1">上一步</el-button>
          <div class="flex gap-3">
            <router-link to="/job"><el-button>取消</el-button></router-link>
            <el-button v-if="currentStep < 2" type="primary" @click="nextStep">下一步</el-button>
            <el-button v-else type="primary" :loading="submitting" @click="submitJob">提交训练任务</el-button>
          </div>
        </div>
      </section>

      <aside class="panel h-fit p-5">
        <p class="text-xs font-semibold uppercase tracking-[0.16em] text-slate-500">本次申请</p>
        <p class="mt-3 text-3xl font-bold text-white">{{ totalGPUs }} <span class="text-base font-medium text-slate-400">GPU</span></p>
        <p class="mt-1 text-xs text-slate-500">{{ form.workerReplicas }} 个训练节点，每个 {{ form.gpusPerWorker }} GPU</p>

        <div class="my-5 border-t border-slate-800" />
        <div class="space-y-4 text-sm">
          <div>
            <p class="text-slate-500">当前步骤</p>
            <p class="mt-1 font-medium text-slate-200">{{ stepTitles[currentStep] }}</p>
          </div>
          <div>
            <p class="text-slate-500">训练方式</p>
            <p class="mt-1 font-medium text-slate-200">{{ executionModeLabel }}</p>
          </div>
          <div>
            <p class="text-slate-500">{{ quotaModel.scopeLabel }}</p>
            <p class="mt-1 font-medium text-slate-200">
              {{ quotaModel.maxWorkerReplicas }} 节点 × {{ quotaModel.maxGpusPerWorker }} 卡，最多 {{ quotaModel.maxTotalGpus }} 卡
            </p>
          </div>
          <div v-if="quotaModel.isTenantScoped" class="grid grid-cols-3 gap-2 border-t border-slate-800 pt-4 text-center">
            <div>
              <p class="text-xs text-slate-500">管理员分配额度</p>
              <p class="mt-1 font-semibold text-slate-200">{{ quotaModel.gpuLimit }} 卡</p>
            </div>
            <div>
              <p class="text-xs text-slate-500">已使用</p>
              <p class="mt-1 font-semibold text-amber-300">{{ quotaModel.gpuUsed }} 卡</p>
            </div>
            <div>
              <p class="text-xs text-slate-500">当前可提交上限</p>
              <p class="mt-1 font-semibold text-emerald-300">{{ quotaModel.gpuAvailable }} 卡</p>
            </div>
          </div>
        </div>

        <div class="mt-6 rounded-xl bg-slate-950/70 p-4 text-xs leading-5 text-slate-400">
          资源是否立刻运行由租户队列和集群可用 GPU 决定。提交成功后可在任务详情查看排队、日志和运行状态。
        </div>
      </aside>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useRoute, useRouter } from 'vue-router'

import { apiPost } from '../../api/client'
import { buildJobSpec } from '../../submission'
import { useJobForm } from '../../composables/useJobForm'
import StepCode from '../../components/job/StepCode.vue'
import StepRuntime from '../../components/job/StepRuntime.vue'
import StepData from '../../components/job/StepData.vue'
import SubmitPreview from '../../components/job/SubmitPreview.vue'

const router = useRouter()
const route = useRoute()
const currentStep = ref(0)
const submitting = ref(false)
const stepTitles = ['代码与环境', '运行规模', '数据与确认']

const {
  form, limits, quotaModel, profiles, executionMode, totalGPUs, commandPreview, commandWarnings, mountPaths,
  trainingImages, workspaceSnapshots, loadingCatalog, applyProfile, toSubmission, stepIssues, loadCatalog,
} = useJobForm(route)

// Element Plus input-number requires max >= min. When quota is zero the form
// keeps its 1 × 1 visual minimum, while quotaModel.blocked remains authoritative.
const formLimits = computed(() => quotaModel.value.blocked
  ? { ...limits.value, maxWorkerReplicas: 1, maxGpusPerWorker: 1 }
  : limits.value)

const fromWorkspaceSnapshot = computed(() => ['dev_workspace', 'workspace_snapshot'].includes(String(route.query.from || '')))
const resumeCheckpointPath = computed(() => String(route.query.checkpointPath || '').trim())
const allIssues = computed(() => [...stepIssues(0), ...stepIssues(1)])

const executionModeLabels = {
  single_gpu: '单卡',
  torchrun: '单机多卡（平台执行 torchrun）',
  ray_train: '多机多卡（Ray 分散放置 + torchrun）',
}
const executionModeLabel = computed(() => executionModeLabels[executionMode.value] || executionMode.value)

const nextStep = () => {
  const issues = stepIssues(currentStep.value)
  if (issues.length) {
    ElMessage.warning(issues[0])
    return
  }
  currentStep.value += 1
}

const submitJob = async () => {
  if (allIssues.value.length) {
    ElMessage.warning(allIssues.value[0])
    return
  }
  submitting.value = true
  try {
    const spec = buildJobSpec(toSubmission())
    const idempotencyKey = globalThis.crypto?.randomUUID?.() || `portal-${Date.now()}`
    const data = await apiPost('/api/v1/jobs', { spec }, { headers: { 'Idempotency-Key': idempotencyKey } })
    ElMessage.success('训练任务已提交，正在等待队列准入')
    router.push(`/job/detail/${data.id}`)
  } catch (error) {
    ElMessage.error(error.message || '提交训练任务失败')
  } finally {
    submitting.value = false
  }
}

onMounted(async () => {
  if (fromWorkspaceSnapshot.value) {
    form.codeSourceType = 'workspace'
    form.workspaceSnapshot = String(route.query.snapshot || '')
  }
  if (resumeCheckpointPath.value) {
    form.checkpoint = { spaceId: 'my-runs', relativePath: resumeCheckpointPath.value }
  }
  // A resubmission carries the previous job's shape so "再来一次" needs no retyping.
  for (const [key, value] of Object.entries({
    name: route.query.name, image: route.query.image, entrypoint: route.query.entrypoint,
  })) {
    if (value) form[key] = String(value)
  }
  await loadCatalog()
})
</script>

<style scoped>
:deep(.role-aware-runtime-step > .mb-6) {
  display: none;
}
</style>
