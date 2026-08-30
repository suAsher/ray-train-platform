<template>
  <div class="rounded-2xl border border-blue-900/60 bg-blue-950/20 p-5">
    <div class="flex items-center justify-between">
      <p class="text-sm font-semibold text-blue-200">提交前确认</p>
      <el-tag v-if="issues.length === 0" size="small" type="success" effect="plain">可以提交</el-tag>
      <el-tag v-else size="small" type="warning" effect="plain">{{ issues.length }} 项待处理</el-tag>
    </div>

    <ul v-if="issues.length" class="mt-4 space-y-1.5">
      <li v-for="issue in issues" :key="issue" class="flex gap-2 text-xs leading-5 text-amber-200">
        <span>•</span><span>{{ issue }}</span>
      </li>
    </ul>

    <dl class="mt-4 grid gap-3 text-sm sm:grid-cols-2">
      <div>
        <dt class="text-slate-500">训练引擎</dt>
        <dd class="mt-1 text-slate-200">{{ engineLabel }}</dd>
      </div>
      <div>
        <dt class="text-slate-500">代码来源</dt>
        <dd class="mt-1 break-all text-slate-200">{{ sourceSummary }}</dd>
      </div>
      <div>
        <dt class="text-slate-500">资源申请</dt>
        <dd class="mt-1 text-slate-200">{{ form.workerReplicas }} 节点 × {{ form.gpusPerWorker }} GPU，共 {{ totalGPUs }} GPU（{{ executionMode }}）</dd>
      </div>
      <div>
        <dt class="text-slate-500">训练镜像</dt>
        <dd class="mt-1 break-all text-slate-200">{{ form.image || '尚未选择' }}</dd>
      </div>
      <div>
        <dt class="text-slate-500">产物目录</dt>
        <dd class="mt-1 break-all text-slate-200">{{ outputSummary }}</dd>
      </div>
      <div>
        <dt class="text-slate-500">数据读取方式</dt>
        <dd class="mt-1 break-all text-slate-200">{{ cacheSummary }}</dd>
      </div>
      <div v-if="form.dataMode === 'streaming'">
        <dt class="text-slate-500">固定数据版本</dt>
        <dd class="mt-1 break-all text-slate-200">{{ fixedDatasetSummary }}</dd>
      </div>
      <div v-if="form.dataMode === 'streaming'">
        <dt class="text-slate-500">提交前检查</dt>
        <dd class="mt-1 text-slate-200">
          <el-tag v-if="preflight" size="small" type="success" effect="plain">已通过</el-tag>
          <span v-else class="text-amber-300">提交时检查镜像、GPU、版本摘要与数据完整性</span>
        </dd>
      </div>
      <div v-if="form.trainingEngine === 'ray-train'">
        <dt class="text-slate-500">托管恢复策略</dt>
        <dd class="mt-1 text-slate-200">{{ managedSummary }}</dd>
      </div>
    </dl>

    <CopyBlock class="mt-4" :text="commandPreview || '（尚未填写启动命令）'" label="平台实际执行" wrap />

    <details class="mt-4">
      <summary class="cursor-pointer text-xs text-slate-400 hover:text-slate-200">用命令行提交同样的任务</summary>
      <CopyBlock class="mt-2" :text="cliCommand" wrap />
    </details>
  </div>
</template>

<script setup>
import { computed } from 'vue'

import { equivalentSubmitCommandPreview } from '../../submission'
import { trainingEngineLabel } from '../../trainingEngine.js'
import { formatDatasetCount } from '../../datasetCatalog.js'
import CopyBlock from '../CopyBlock.vue'

const props = defineProps({
  form: { type: Object, required: true },
  issues: { type: Array, default: () => [] },
  totalGPUs: { type: Number, default: 0 },
  executionMode: { type: String, default: 'single_gpu' },
  commandPreview: { type: String, default: '' },
  cachePolicy: { type: Object, default: () => ({}) },
  preflight: { type: Object, default: null },
})

const sourceSummary = computed(() => {
  if (props.form.codeSourceType === 'git') {
    if (!props.form.gitURL) return '尚未填写 Git 仓库'
    return props.form.gitCommit ? `${props.form.gitURL} @ ${props.form.gitCommit.slice(0, 12)}` : `${props.form.gitURL}（尚未解析 Commit）`
  }
  return props.form.workspaceSnapshot || '尚未选择工作区快照'
})

const engineLabel = computed(() => trainingEngineLabel(props.form.trainingEngine))
const managedSummary = computed(() => (
  `最多恢复 ${Number(props.form.maxFailures || 0)} 次；每 ${Number(props.form.checkpointEveryEpochs || 0)} Epoch 保存，` +
  `保留最近 ${Number(props.form.checkpointKeepLatest || 0)} / 最佳 ${Number(props.form.checkpointKeepBest || 0)}`
))

const outputSummary = computed(() => (props.form.output?.spaceId === 'my-runs'
  ? '我的训练结果 · 平台自动创建独立 runs/<job-id>'
  : '尚未选择训练结果空间'))

const cacheSummary = computed(() => {
  const labels = {
    mount: '直接读取 TOS/IDC',
    cache: 'NVMe 预热',
    'ray-data-stage': 'Ray Data 分布式预热 + NVMe',
    streaming: '版本化 Ray Data 流式读取',
  }
  const label = labels[props.form.dataMode] || '直接读取 TOS/IDC'
  if (props.form.dataMode === 'streaming') {
    const policies = { auto: '平台自动选择缓存', off: '关闭工作集缓存', bounded: '双 NVMe 有界工作集' }
    return `${label} · ${policies[props.form.datasetCachePolicy] || policies.auto}`
  }
  if (props.form.cacheMode !== 'runtime') return label
  const size = String(props.form.cacheSize || '').trim() || '尚未选择容量'
  const mountPath = String(props.cachePolicy.mountPath || '').trim() || '挂载路径未提供'
  const preload = props.form.cachePreload === 'input' ? ' · 自动预热所选输入' : ''
  return `${label} · ${size} · 挂载到 ${mountPath}${preload}`
})

const fixedDatasetSummary = computed(() => {
  const dataset = props.preflight?.dataset
  if (dataset) {
    return `${dataset.datasetSlug || dataset.datasetId} · ${dataset.version} · ${formatDatasetCount(dataset.trainSamples)} train`
  }
  const selected = props.form.datasetRef || {}
  if (!selected.dataset) return '尚未选择数据集'
  return selected.version === 'latest'
    ? `${selected.dataset} · 最新可用（提交时固定为具体版本）`
    : `${selected.dataset} · ${selected.version}`
})

const cliCommand = computed(() => equivalentSubmitCommandPreview(props.form))
</script>
