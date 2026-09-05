import { computed, reactive, ref, watch } from 'vue'

import {
  clampResources,
  containerPathFor,
  defaultPlatformLimits,
  jobQuotaModel,
  normalizeCachePolicy,
  normalizeCacheSelection,
  profilesFromLimits,
  resolveExecutionMode,
} from '../platformLimits.js'
import { entrypointWarnings, previewCommand } from '../commandPreview.js'
import {
  managedEngineAvailability,
  managedPolicyFromQuery,
  managedPolicyIssues,
  normalizeTrainingEngine,
} from '../trainingEngine.js'
import {
  normalizeDatasetCapabilities,
  streamingDatasetAvailability,
} from '../datasetCatalog.js'
import { jobFormStepIssues } from './jobFormIssues.js'

const datasetCachePolicies = new Set(['auto', 'off', 'bounded'])

/**
 * State and rules for the submit form.
 *
 * Extracted from CreateJob.vue so the view renders and this file decides. The
 * execution mode is derived rather than stored: keeping it as separate state
 * is what let a user pick the single-GPU card, raise the GPU count, and only
 * discover the mismatch when the server rejected the submit.
 */
export function useJobForm(route, catalogLoaders = defaultCatalogLoaders) {
  const limits = ref(defaultPlatformLimits)
  const trainingImages = ref([])
  const workspaceSnapshots = ref([])
  const loadingCatalog = ref(false)

  const copiedManagedPolicy = managedPolicyFromQuery(route?.query)
  const form = reactive({
    name: '',
    image: '',
    codeSourceType: 'git',
    gitURL: '',
    gitRef: '',
    gitCommit: '',
    workspaceSnapshot: '',
    sourceArtifactId: '',
    entrypoint: 'python train.py',
    trainingEngine: normalizeTrainingEngine(route?.query?.trainingEngine),
    dataMode: String(route?.query?.dataMode || 'mount'),
    datasetRef: {
      dataset: String(route?.query?.dataset || ''),
      version: String(route?.query?.datasetVersion || 'latest') || 'latest',
      ...(route?.query?.datasetSites ? { sites: String(route.query.datasetSites).split(',').map((site) => site.trim()) } : {}),
    },
    datasetCachePolicy: datasetCachePolicies.has(String(route?.query?.cachePolicy || ''))
      ? String(route.query.cachePolicy)
      : 'auto',
    ...copiedManagedPolicy,
    parentJobId: String(route?.query?.parentJobId || ''),
    workerReplicas: 1,
    gpusPerWorker: 1,
    cpuPerWorker: 8,
    memoryPerWorker: '32Gi',
    timeoutSeconds: 0,
    maxRetries: 0,
    cacheMode: 'off',
    cacheSize: '',
    cachePreload: '',
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
  const managedAvailability = computed(() => managedEngineAvailability({
    limits: limits.value,
    images: trainingImages.value,
    imageReference: form.image,
  }))
  const datasetCapabilities = computed(() => normalizeDatasetCapabilities(limits.value.datasets))
  const streamingAvailability = computed(() => streamingDatasetAvailability({
    limits: limits.value,
    images: trainingImages.value,
    imageReference: form.image,
  }))

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

  watch(
    () => limits.value.cache,
    (cachePolicy) => {
      const normalized = normalizeCacheSelection(form, cachePolicy, { selectRuntimeDefault: true })
      form.cacheMode = normalized.cacheMode
      form.cacheSize = normalized.cacheSize
      if (normalized.cacheMode !== 'runtime') form.cachePreload = ''
      if (normalized.cacheMode !== 'runtime' && !['mount', 'streaming'].includes(form.dataMode)) form.dataMode = 'mount'
    },
    { deep: true },
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
      if (form.codeSourceType === 'workspace-archive' && !String(form.sourceArtifactId).trim()) {
        issues.push('请先上传一个不可变代码包')
      }
    }
    if (step === 1) {
      if (form.trainingEngine === 'ray-train' && !managedAvailability.value.available) {
        issues.push(managedAvailability.value.reason)
      }
      if (form.dataMode === 'streaming' && !streamingAvailability.value.available) {
        issues.push(streamingAvailability.value.reason)
      }
      if (form.trainingEngine === 'ray-train') issues.push(...managedPolicyIssues(form))
      issues.push(...jobFormStepIssues({
        step,
        form,
        limits: limits.value,
        commandWarnings: commandWarnings.value,
      }))
    }
    if (step === 2 && form.dataMode === 'streaming') {
      if (!String(form.datasetRef?.dataset || '').trim()) issues.push('请选择数据集')
      if (!String(form.datasetRef?.version || '').trim()) issues.push('请选择可训练的数据版本')
    }
    return issues
  }

  const loadCatalog = async () => {
    loadingCatalog.value = true
    const results = await Promise.allSettled([
      catalogLoaders.fetchPlatformLimits(),
      catalogLoaders.fetchImages('training'),
      catalogLoaders.fetchWorkspaceSnapshots(),
    ])
    const [limitsResult, imagesResult, snapshotsResult] = results
    if (limitsResult.status === 'fulfilled' && limitsResult.value) {
      limits.value = {
        ...defaultPlatformLimits,
        ...limitsResult.value,
        cache: normalizeCachePolicy(limitsResult.value.cache),
        datasets: normalizeDatasetCapabilities(limitsResult.value.datasets),
      }
      const bounded = clampResources({ workers: form.workerReplicas, gpus: form.gpusPerWorker }, limits.value)
      form.workerReplicas = bounded.workers
      form.gpusPerWorker = bounded.gpus
    }
    const copiedCache = normalizeCacheSelection({
      cacheMode: route?.query?.cacheMode,
      cacheSize: route?.query?.cacheSize,
    }, limits.value.cache)
    form.cacheMode = copiedCache.cacheMode
    form.cacheSize = copiedCache.cacheSize
    form.cachePreload = copiedCache.cacheMode === 'runtime' && route?.query?.cachePreload === 'input' ? 'input' : ''
    form.dataMode = String(route?.query?.dataMode || (form.cachePreload === 'input' ? 'cache' : 'mount'))
    trainingImages.value = imagesResult.status === 'fulfilled' ? imagesResult.value || [] : []
    workspaceSnapshots.value = snapshotsResult.status === 'fulfilled' ? snapshotsResult.value || [] : []
    const canaryRayVersion = String(limits.value.runtime?.canaryRayVersion || '2.58.0').trim()
    const streamingPreferred = form.dataMode === 'streaming'
      ? trainingImages.value.find((image) =>
          String(image?.rayVersion || '').trim() === canaryRayVersion &&
          Array.isArray(image?.supportedEngines) && image.supportedEngines.includes('ray-train'))
      : null
    const preferred = streamingPreferred || trainingImages.value.find((image) => image.isDefault) || trainingImages.value[0]
    if (preferred && !form.image) form.image = preferred.reference
    if (form.dataMode === 'streaming' && !streamingAvailability.value.available) {
      form.dataMode = 'mount'
      form.datasetCachePolicy = 'auto'
    }
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
    managedAvailability,
    datasetCapabilities,
    streamingAvailability,
    workspaceSnapshots,
    loadingCatalog,
    applyProfile,
    toSubmission,
    stepIssues,
    loadCatalog,
  }
}

const defaultCatalogLoaders = Object.freeze({
  fetchPlatformLimits: (...arguments_) => import('../api/platform.js').then(({ fetchPlatformLimits }) => fetchPlatformLimits(...arguments_)),
  fetchImages: (...arguments_) => import('../api/catalog.js').then(({ fetchImages }) => fetchImages(...arguments_)),
  fetchWorkspaceSnapshots: (...arguments_) => import('../api/dataSpaces.js').then(({ fetchWorkspaceSnapshots }) => fetchWorkspaceSnapshots(...arguments_)),
})
