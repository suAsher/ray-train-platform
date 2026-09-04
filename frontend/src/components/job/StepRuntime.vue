<template>
  <div>
    <div class="mb-6">
      <h2 class="text-lg font-semibold text-white">运行规模</h2>
      <p class="mt-1 text-sm text-slate-400">
        选择训练方式，平台会据此决定命令在哪里、以什么并行方式运行。当前集群上限：{{ limits.maxWorkerReplicas }} 个节点 ×
        {{ limits.maxGpusPerWorker }} 卡，单任务最多 {{ limits.maxTotalGpus }} 卡。
      </p>
    </div>

    <fieldset class="mb-6">
      <legend class="field-label">训练引擎</legend>
      <div class="mt-2 grid gap-3 sm:grid-cols-2">
        <button
          type="button"
          class="rounded-2xl border p-4 text-left transition"
          :class="engineClass('ray-ddp')"
          :aria-pressed="form.trainingEngine !== 'ray-train'"
          @click="selectTrainingEngine('ray-ddp')"
        >
          <div class="flex items-center justify-between gap-3">
            <p class="font-semibold text-white">Ray 编排 DDP</p>
            <el-tag size="small" effect="plain">默认</el-tag>
          </div>
          <p class="mt-2 text-xs leading-5 text-slate-400">保持现有训练代码与启动方式，由平台在 Ray Worker 上编排 torchrun。</p>
        </button>
        <button
          type="button"
          class="rounded-2xl border p-4 text-left transition"
          :class="engineClass('ray-train')"
          :disabled="!managedAvailability.available"
          :aria-disabled="!managedAvailability.available"
          :aria-pressed="form.trainingEngine === 'ray-train'"
          :aria-describedby="!managedAvailability.available ? 'managed-engine-reason' : undefined"
          @click="selectTrainingEngine('ray-train')"
        >
          <div class="flex items-center justify-between gap-3">
            <p class="font-semibold text-white">Ray Train 托管</p>
            <el-tag size="small" type="success" effect="plain">托管恢复</el-tag>
          </div>
          <p class="mt-2 text-xs leading-5 text-slate-400">由 Ray Train 管理 Worker 恢复、原生指标与 Checkpoint 生命周期。</p>
          <p
            v-if="!managedAvailability.available"
            id="managed-engine-reason"
            class="mt-3 text-xs text-amber-300"
          >{{ managedAvailability.reason }}</p>
        </button>
      </div>
      <el-alert
        v-if="form.trainingEngine === 'ray-train' && !managedAvailability.available"
        id="managed-engine-blocking-alert"
        class="mt-3"
        type="error"
        show-icon
        :closable="false"
        :title="managedAvailability.reason"
      />
    </fieldset>

    <div v-if="form.trainingEngine === 'ray-train'" class="mb-6 rounded-2xl border border-emerald-900/60 bg-emerald-950/15 p-4">
      <p class="text-sm font-semibold text-emerald-200">托管恢复与 Checkpoint 策略</p>
      <div class="mt-4 grid gap-4 sm:grid-cols-2">
        <div>
          <label class="field-label">Worker 最大恢复次数</label>
          <el-input-number v-model="form.maxFailures" :min="0" :max="10" class="w-full" />
        </div>
        <div>
          <label class="field-label">每隔多少 Epoch 保存</label>
          <el-input-number v-model="form.checkpointEveryEpochs" :min="0" :max="100000" class="w-full" />
          <p class="field-help">填 0 表示不按 Epoch 自动保存。</p>
        </div>
        <div>
          <label class="field-label">保留最近 Checkpoint</label>
          <el-input-number v-model="form.checkpointKeepLatest" :min="0" :max="1000" class="w-full" />
        </div>
        <div>
          <label class="field-label">保留最佳 Checkpoint</label>
          <el-input-number v-model="form.checkpointKeepBest" :min="0" :max="1000" class="w-full" />
        </div>
      </div>
    </div>

    <div class="grid gap-3 sm:grid-cols-3">
      <button
        v-for="profile in profiles"
        :key="profile.executionMode"
        type="button"
        class="rounded-2xl border p-4 text-left transition"
        :class="profileClass(profile)"
        :disabled="!profile.available"
        @click="$emit('apply-profile', profile)"
      >
        <p class="font-semibold text-white">{{ profile.name }}</p>
        <p class="mt-1 text-xs leading-5 text-slate-400">{{ profile.description }}</p>
        <p v-if="profile.available" class="mt-3 text-sm font-semibold text-blue-300">
          {{ profile.workers }} 节点 × {{ profile.gpus }} GPU
        </p>
        <p v-else class="mt-3 text-xs text-slate-500">{{ profile.unavailableReason || '当前集群暂不支持' }}</p>
      </button>
    </div>

    <div class="mt-6">
      <label class="field-label">训练启动命令</label>
      <el-input v-model="form.entrypoint" type="textarea" :rows="3" placeholder="例如：python tools/train.py configs/lidar.yaml --launcher pytorch" />
      <p class="field-help">命令在 <code>{{ workspacePath }}</code> 下执行，只能是一条命令。带空格的参数请用引号。</p>
      <p class="field-help">
        数据和结果路径请在代码里用 <code>os.environ["PLATFORM_DATASET_PATH"]</code>、<code>os.environ["PLATFORM_OUTPUT_PATH"]</code> 读取。
        写在启动命令里的 <code>$PLATFORM_*</code> 可能在提交侧就被求值成空字符串，训练随后会向根路径写入并报 PermissionError。
        <router-link class="text-blue-400 hover:text-blue-300" to="/help#contract">查看对接契约与示例</router-link>
      </p>
    </div>

    <div class="mt-4 rounded-2xl border border-slate-800 bg-slate-950/60 p-4">
      <div class="mb-3 flex items-center justify-between">
        <p class="text-xs font-semibold uppercase tracking-wider text-slate-400">平台实际执行</p>
        <el-tag size="small" effect="plain" type="info">{{ executionMode }}</el-tag>
      </div>
      <CopyBlock :text="commandPreview || '（填写启动命令后显示）'" wrap />
      <p v-if="executionMode !== 'single_gpu'" class="mt-2 text-[11px] leading-5 text-slate-500">
        平台在所选 GPU 上自动启动 torchrun，你的命令不需要也不应该再写 torchrun。
      </p>
    </div>

    <el-alert v-for="warning in warnings" :key="warning" class="mt-3" type="warning" show-icon :closable="false" :title="warning" />

    <div class="mt-5 rounded-2xl border border-slate-800 bg-slate-950/40 p-4">
      <label class="field-label">数据读取方式</label>
      <p class="field-help">
        不确定就选「直接读取」。先跑通，再从训练日志里确认数据读取确实是瓶颈（<code>data_time</code> 明显不为 0），才需要换其他方式——换错不会更快，只会多一层复杂度。
        <router-link class="text-blue-400 hover:text-blue-300" to="/help#data-mode">怎么选</router-link>
      </p>
      <div class="mt-3 grid gap-3 lg:grid-cols-2 xl:grid-cols-4">
        <button type="button" class="rounded-xl border p-3 text-left transition" :class="dataModeClass('mount')" @click="selectDataMode('mount')">
          <p class="text-sm font-semibold text-white">直接读取</p>
          <p class="mt-1 text-xs leading-5 text-slate-400">从已授权的 TOS/IDC 挂载读取，启动最快；大量小文件可能受远端元数据延迟影响。</p>
        </button>
        <button type="button" class="rounded-xl border p-3 text-left transition" :class="dataModeClass('cache')" :disabled="!runtimeCacheAvailable" @click="selectDataMode('cache')">
          <p class="text-sm font-semibold text-white">NVMe 预热</p>
          <p class="mt-1 text-xs leading-5 text-slate-400">Worker 启动前复制所选目录到两块本地盘，保留现有训练代码和 DataLoader。</p>
        </button>
        <button
          type="button"
          class="rounded-xl border p-3 text-left transition"
          :class="dataModeClass('ray-data-stage')"
          :disabled="!runtimeCacheAvailable || !managedAvailability.available"
          @click="selectDataMode('ray-data-stage')"
        >
          <div class="flex items-center justify-between gap-2">
            <p class="text-sm font-semibold text-white">Ray Data + NVMe</p>
            <el-tag size="small" type="success" effect="plain">推荐小文件</el-tag>
          </div>
          <p class="mt-1 text-xs leading-5 text-slate-400">Ray Data 分布式读取，每个训练节点生成完整本地视图，再交给原 DataLoader。</p>
        </button>
        <button
          type="button"
          class="rounded-xl border p-3 text-left transition"
          :class="dataModeClass('streaming')"
          :disabled="!streamingAvailability.available"
          :aria-disabled="!streamingAvailability.available"
          @click="selectDataMode('streaming')"
        >
          <div class="flex items-center justify-between gap-2">
            <p class="text-sm font-semibold text-white">版本化流式</p>
            <el-tag size="small" type="success" effect="plain">Ray Data</el-tag>
          </div>
          <p class="mt-1 text-xs leading-5 text-slate-400">选择不可变数据版本，Ray Train 按 Worker 分片流式读取；可自动使用双 NVMe 有界工作集。</p>
          <p v-if="!streamingAvailability.available" class="mt-2 text-xs text-amber-300">{{ streamingAvailability.reason }}</p>
        </button>
      </div>
      <el-select
        v-if="['cache', 'ray-data-stage'].includes(form.dataMode)"
        :model-value="form.cacheSize"
        class="mt-3 w-full"
        placeholder="选择缓存容量"
        @change="selectCacheSize"
      >
        <el-option v-for="size in cachePolicy.allowedSizes" :key="size" :label="size" :value="size" />
      </el-select>
      <el-alert
        v-if="['cache', 'ray-data-stage'].includes(form.dataMode) && !hasExactInput"
        class="mt-3" type="warning" show-icon :closable="false"
        title="请先在数据步骤选择一个具体的数据集子目录；不能预热整个 public、团队或个人根目录。"
      />
      <p class="field-help">
        旧的预热模式会生成任务级完整本地视图；版本化流式模式只缓存当前工作集，并由平台固定数据版本。输出和 Checkpoint 始终写入持久存储。
      </p>
      <p class="field-help">
        “NVMe 预热”和“Ray Data + NVMe”使用随任务结束释放的一次性缓存，预热进度会在任务状态与日志中展示；它不是 Ray 临时文件，也不是 object spill。
      </p>
    </div>

    <el-collapse class="mt-5 border-slate-800">
      <el-collapse-item title="调整 CPU、内存与运行控制" name="advanced">
        <div class="grid gap-4 py-3 sm:grid-cols-2">
          <div>
            <label class="field-label">节点数量</label>
            <el-input-number v-model="form.workerReplicas" :min="1" :max="limits.maxWorkerReplicas" class="w-full" />
          </div>
          <div>
            <label class="field-label">每节点 GPU</label>
            <el-input-number v-model="form.gpusPerWorker" :min="1" :max="limits.maxGpusPerWorker" class="w-full" />
          </div>
          <div>
            <label class="field-label">每节点 CPU</label>
            <el-input-number v-model="form.cpuPerWorker" :min="1" :max="256" class="w-full" />
          </div>
          <div>
            <label class="field-label">每节点内存</label>
            <el-input v-model="form.memoryPerWorker" placeholder="例如：128Gi" />
          </div>
          <div>
            <label class="field-label">最长运行时间（秒）</label>
            <el-input-number v-model="form.timeoutSeconds" :min="0" :max="604800" class="w-full" />
            <p class="field-help">填 0 表示不设置平台层超时。</p>
          </div>
          <div>
            <label class="field-label">提交失败重试</label>
            <el-select v-model="form.maxRetries" class="w-full">
              <el-option :value="0" label="不自动重试" />
              <el-option :value="1" label="最多重试 1 次" />
              <el-option :value="2" label="最多重试 2 次" />
              <el-option :value="3" label="最多重试 3 次" />
            </el-select>
            <p class="field-help">
              只覆盖镜像拉取、节点中断这类提交期故障，会从头重跑，<strong class="text-amber-300">不是断点续训</strong>。
              需要接着上次训练，请在任务详情使用“续训”。
            </p>
          </div>
        </div>
      </el-collapse-item>
    </el-collapse>
  </div>
</template>

<script setup>
import { computed } from 'vue'

import { normalizeCachePolicy, normalizeCacheSelection } from '../../platformLimits'
import CopyBlock from '../CopyBlock.vue'

const props = defineProps({
  form: { type: Object, required: true },
  profiles: { type: Array, default: () => [] },
  limits: { type: Object, required: true },
  executionMode: { type: String, default: 'single_gpu' },
  commandPreview: { type: String, default: '' },
  warnings: { type: Array, default: () => [] },
  workspacePath: { type: String, default: '/workspace' },
  managedAvailability: {
    type: Object,
    default: () => ({ available: false, reason: '当前团队未开放 Ray Train 托管' }),
  },
  streamingAvailability: {
    type: Object,
    default: () => ({ available: false, reason: '当前团队未开放版本化流式训练' }),
  },
})

defineEmits(['apply-profile'])

const cachePolicy = computed(() => normalizeCachePolicy(props.limits.cache))
const runtimeCacheAvailable = computed(() =>
  cachePolicy.value.modes.includes('runtime') && cachePolicy.value.allowedSizes.length > 0)
const hasExactInput = computed(() => Boolean(String(props.form.input?.spaceId || '').trim() && String(props.form.input?.relativePath || '').trim()))

const applyCacheSelection = (selection, selectRuntimeDefault = false) => {
  const normalized = normalizeCacheSelection(selection, cachePolicy.value, { selectRuntimeDefault })
  props.form.cacheMode = normalized.cacheMode
  props.form.cacheSize = normalized.cacheSize
  if (normalized.cacheMode !== 'runtime') props.form.cachePreload = ''
}

const selectCacheSize = (cacheSize) => {
  applyCacheSelection({ cacheMode: 'runtime', cacheSize })
}

const selectDataMode = (mode) => {
  if ((mode === 'cache' || mode === 'ray-data-stage') && !runtimeCacheAvailable.value) return
  if (mode === 'streaming' && !props.streamingAvailability.available) return
  if (mode === 'ray-data-stage') {
    if (!props.managedAvailability.available) return
    props.form.trainingEngine = 'ray-train'
  }
  if (mode === 'streaming') {
    props.form.trainingEngine = 'ray-train'
    applyCacheSelection({ cacheMode: 'off', cacheSize: '' })
    props.form.cachePreload = ''
    if (!['auto', 'off', 'bounded'].includes(props.form.datasetCachePolicy)) props.form.datasetCachePolicy = 'auto'
  }
  props.form.dataMode = mode
  if (mode === 'mount') {
    applyCacheSelection({ cacheMode: 'off', cacheSize: '' })
    props.form.cachePreload = ''
    return
  }
  if (mode === 'streaming') return
  applyCacheSelection({ cacheMode: 'runtime', cacheSize: props.form.cacheSize }, true)
  props.form.cachePreload = mode === 'cache' ? 'input' : ''
}

const selectTrainingEngine = (engine) => {
  if (engine === 'ray-train' && !props.managedAvailability.available) return
  props.form.trainingEngine = engine
  if (engine !== 'ray-train' && ['ray-data-stage', 'streaming'].includes(props.form.dataMode)) selectDataMode('mount')
}

const dataModeClass = (mode) => {
  const disabled = (mode === 'cache' || mode === 'ray-data-stage') && !runtimeCacheAvailable.value
    || mode === 'ray-data-stage' && !props.managedAvailability.available
    || mode === 'streaming' && !props.streamingAvailability.available
  if (disabled) return 'cursor-not-allowed border-slate-800 bg-slate-900/30 opacity-60'
  return props.form.dataMode === mode
    ? 'border-blue-400 bg-blue-950/30'
    : 'border-slate-700 bg-slate-900/50 hover:border-slate-500'
}

const engineClass = (engine) => {
  const disabled = engine === 'ray-train' && !props.managedAvailability.available
  if (disabled) return 'cursor-not-allowed border-slate-800 bg-slate-900/30 opacity-60'
  const selected = engine === 'ray-train'
    ? props.form.trainingEngine === 'ray-train'
    : props.form.trainingEngine !== 'ray-train'
  return selected ? 'border-emerald-400 bg-emerald-950/20' : 'border-slate-700 bg-slate-900/50 hover:border-slate-500'
}

const profileClass = (profile) => {
  if (!profile.available) return 'cursor-not-allowed border-slate-800 bg-slate-900/30 opacity-60'
  const selected = Number(props.form.workerReplicas) === profile.workers && Number(props.form.gpusPerWorker) === profile.gpus
  return selected ? 'border-blue-400 bg-blue-950/30' : 'border-slate-700 bg-slate-900/50 hover:border-slate-500'
}
</script>
