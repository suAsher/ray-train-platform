import { computed, reactive, ref, watch } from 'vue'

import { fetchPlatformLimits } from '../api/platform'
import { fetchImages } from '../api/catalog'
import { fetchWorkspaceSnapshots } from '../api/dataSpaces'
import { clampResources, containerPathFor, defaultPlatformLimits, jobQuotaModel, profilesFromLimits, resolveExecutionMode } from '../platformLimits'
import { entrypointWarnings, previewCommand } from '../commandPreview'
import { jobFormStepIssues } from './jobFormIssues.js'

/**
 * State and rules for the submit form.
 *
 * Extracted from CreateJob.vue so the view renders and this file decides. The
 * execution mode is derived rather than stored: keeping it as separate state
 * is what let a user pick the single-GPU card, raise the GPU count, and only
 * discover the mismatch when the server rejected the submit.
 */
export function useJobForm(route) {
  const limits = ref(defaultPlatformLimits)
  const trainingImages = ref([])
  const workspaceSnapshots = ref([])
  const loadingCatalog = ref(false)

  const form = reactive({
    name: '',
    image: '',
    codeSourceType: 'git',
    gitURL: '',
    gitRef: '',
    gitCommit: '',
    workspaceSnapshot: '',
    entrypoint: 'python train.py',
    workerReplicas: 1,
    gpusPerWorker: 1,
    cpuPerWorker: 8,
    memoryPerWorker: '32Gi',
    timeoutSeconds: 0,
    maxRetries: 0,
    input: { spaceId: String(route?.query?.dataSpace || ''), relativePath: String(route?.query?.dataPath || '') },
    checkpoint: {},
    output: {},
  })

  const profiles = computed(() => profilesFromLimits(limits.value))
  const quotaModel = computed(() => jobQuotaModel(limits.value))
  const executionMode = computed(() => resolveExecutionMode(form.workerReplicas, form.gpusPerWorker))
  const totalGPUs = computed(() => Number(form.workerReplicas || 0) * Number(form.gpusPerWorker || 0))
  const commandPreview = computed(() => previewCommand(form.entrypoint, form.workerReplicas, form.gpusPerWorker))
  const commandWarnings = computed(() => entrypointWarnings(form.entrypoint, form.workerReplicas, form.gpusPerWorker))
  const mountPaths = computed(() => ({
    dataset: containerPathFor('dataset', limits.value),
    checkpoint: containerPathFor('checkpoint', limits.value),
    output: containerPathFor('output', limits.value),
    workspace: containerPathFor('workspace', limits.value),
  }))

  const activeProfile = computed(() => profiles.value.find((profile) =>
    profile.workers === Number(form.workerReplicas) && profile.gpus === Number(form.gpusPerWorker)))

  // A shape beyond the deployment ceiling is corrected as the user types rather
  // than accepted and rejected later by the API.
  watch(
    () => [form.workerReplicas, form.gpusPerWorker],
    () => {
      const bounded = clampResources({ workers: form.workerReplicas, gpus: form.gpusPerWorker }, limits.value)
      if (bounded.workers !== Number(form.workerReplicas)) form.workerReplicas = bounded.workers
      if (bounded.gpus !== Number(form.gpusPerWorker)) form.gpusPerWorker = bounded.gpus
    },
  )

  const applyProfile = (profile) => {
    if (!profile?.available) return
    form.workerReplicas = profile.workers
    form.gpusPerWorker = profile.gpus
    form.cpuPerWorker = profile.cpu
    form.memoryPerWorker = profile.memory
  }

  /** Values the submit builder needs, including the derived execution mode. */
  const toSubmission = () => ({ ...form, executionMode: executionMode.value })

  const stepIssues = (step) => {
    const issues = []
    if (step === 0) {
      if (!String(form.name).trim()) issues.push('请填写任务名称')
      if (!String(form.image).trim()) issues.push('请选择训练镜像')
      if (form.codeSourceType === 'git') {
        if (!String(form.gitURL).trim()) issues.push('请填写 Git 仓库地址')
        if (!String(form.gitCommit).trim()) issues.push('请填写分支或 Commit 并完成解析')
      }
      if (form.codeSourceType === 'workspace' && !String(form.workspaceSnapshot).trim()) {
        issues.push('请选择一个工作区代码版本')
      }
    }
    if (step === 1) {
      issues.push(...jobFormStepIssues({
        step,
        form,
        limits: limits.value,
        commandWarnings: commandWarnings.value,
      }))
    }
    return issues
  }

  const loadCatalog = async () => {
    loadingCatalog.value = true
    const results = await Promise.allSettled([fetchPlatformLimits(), fetchImages('training'), fetchWorkspaceSnapshots()])
    const [limitsResult, imagesResult, snapshotsResult] = results
    if (limitsResult.status === 'fulfilled' && limitsResult.value) {
      limits.value = { ...defaultPlatformLimits, ...limitsResult.value }
      const bounded = clampResources({ workers: form.workerReplicas, gpus: form.gpusPerWorker }, limits.value)
      form.workerReplicas = bounded.workers
      form.gpusPerWorker = bounded.gpus
    }
    trainingImages.value = imagesResult.status === 'fulfilled' ? imagesResult.value || [] : []
    workspaceSnapshots.value = snapshotsResult.status === 'fulfilled' ? snapshotsResult.value || [] : []
    const preferred = trainingImages.value.find((image) => image.isDefault) || trainingImages.value[0]
    if (preferred && !form.image) form.image = preferred.reference
    loadingCatalog.value = false
  }

  return {
    form,
    limits,
    quotaModel,
    profiles,
    activeProfile,
    executionMode,
    totalGPUs,
    commandPreview,
    commandWarnings,
    mountPaths,
    trainingImages,
    workspaceSnapshots,
    loadingCatalog,
    applyProfile,
    toSubmission,
    stepIssues,
    loadCatalog,
  }
}
