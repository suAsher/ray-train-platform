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

    <div v-if="cachePolicy.enabled" class="mt-5 rounded-2xl border border-slate-800 bg-slate-950/40 p-4">
      <label class="field-label">一次性运行时缓存</label>
      <el-radio-group :model-value="form.cacheMode" class="mt-2" @change="selectCacheMode">
        <el-radio-button value="off">关闭</el-radio-button>
        <el-radio-button value="runtime" :disabled="!runtimeCacheAvailable">运行时缓存</el-radio-button>
      </el-radio-group>
      <el-select
        v-if="form.cacheMode === 'runtime'"
        :model-value="form.cacheSize"
        class="mt-3 w-full"
        placeholder="选择缓存容量"
        @change="selectCacheSize"
      >
        <el-option v-for="size in cachePolicy.allowedSizes" :key="size" :label="size" :value="size" />
      </el-select>
      <div v-if="form.cacheMode === 'runtime'" class="mt-4 rounded-xl border border-blue-900/60 bg-blue-950/20 p-3">
        <div class="flex items-center justify-between gap-4">
          <div>
            <p class="text-sm font-medium text-blue-100">自动预热所选输入到双 NVMe</p>
            <p class="mt-1 text-xs leading-5 text-slate-400">每个 Worker 在训练启动前复制一份，只需选择具体的数据集子目录，模型代码不负责复制。</p>
          </div>
          <el-switch :model-value="form.cachePreload === 'input'" @change="selectCachePreload" />
        </div>
        <el-alert
          v-if="form.cachePreload === 'input' && !hasExactInput"
          class="mt-3" type="warning" show-icon :closable="false"
          title="请先在数据步骤选择一个具体的数据集子目录；不能预热整个 public、团队或个人根目录。"
        />
      </div>
      <p class="field-help">
        这是随任务结束释放的一次性缓存。不开启自动预热时，仅用于 Ray 临时文件、object spill，以及训练代码显式写入
        <code>{{ cachePolicy.mountPath }}</code> 的内容；开启后，平台会把所选输入分片到两块 NVMe 并自动切换训练数据路径。
        冷启动会增加一次复制时间；输出和 Checkpoint 始终写入持久存储。
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

const selectCacheMode = (cacheMode) => {
  applyCacheSelection({ cacheMode, cacheSize: props.form.cacheSize }, cacheMode === 'runtime')
}

const selectCacheSize = (cacheSize) => {
  applyCacheSelection({ cacheMode: 'runtime', cacheSize })
}

const selectCachePreload = (enabled) => {
  props.form.cachePreload = enabled ? 'input' : ''
}

const selectTrainingEngine = (engine) => {
  if (engine === 'ray-train' && !props.managedAvailability.available) return
  props.form.trainingEngine = engine
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
