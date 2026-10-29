<template>
  <div>
    <div class="mb-6">
      <h2 class="text-lg font-semibold text-white">数据与产物</h2>
      <p class="mt-1 text-sm text-slate-400">只选择已授权的数据根目录；平台会在 Ray Head 和所有 Worker 中挂载相同位置。</p>
    </div>

    <el-alert v-if="form.dataMode !== 'streaming'" type="info" :closable="false" class="mb-6">
      <template #title>
        训练容器不持有 TOS 密钥。脚本只读取平台注入的环境变量：<code>PLATFORM_DATASET_PATH</code>、
        <code>PLATFORM_CHECKPOINT_PATH</code>、<code>PLATFORM_OUTPUT_PATH</code>。
      </template>
    </el-alert>

    <el-alert v-else type="success" :closable="false" class="mb-6">
      <template #title>
        选择平台已发布的数据集版本。训练任务只保存逻辑版本与摘要，不需要填写底层存储路径。
      </template>
    </el-alert>

    <el-checkbox v-model="useDataSpaces" class="mb-4">使用可选对象存储数据空间（需要管理员配置）</el-checkbox>
    <div v-if="!useDataSpaces" class="space-y-5">
      <StorageDirectoryPicker v-model="form.datasetStorage" kind="dataset" label="训练输入 PVC" help="选择管理员登记的只读训练数据。" />
      <StorageDirectoryPicker v-model="form.checkpointStorage" kind="checkpoint" label="Checkpoint PVC" help="可选的只读初始权重。" />
      <StorageDirectoryPicker v-model="form.outputStorage" kind="output" label="训练结果 PVC" help="选择可写的训练产物存储。" output />
    </div>
    <div v-else class="space-y-5">
      <DataSpacePicker
        v-if="form.dataMode !== 'streaming'"
        v-model="form.input"
        label="训练输入"
        help="选择数据从哪里读取。团队和公共数据均只读；可先在“我的数据”中浏览目录。"
        :container-path="mountPaths.dataset"
        env-variable="PLATFORM_DATASET_PATH"
      />
      <DatasetVersionPicker
        v-else
        v-model="form.datasetRef"
        v-model:cache-policy="form.datasetCachePolicy"
      />
      <DataSpacePicker
        v-model="form.checkpoint"
        label="初始 Checkpoint"
        help="可选。选择基础模型、权重或上一次训练结果，运行时保持只读。"
        :container-path="mountPaths.checkpoint"
        env-variable="PLATFORM_CHECKPOINT_PATH"
      />
      <DataSpacePicker
        v-model="form.output"
        label="训练结果"
        help="每个任务自动创建独立结果目录；完成后可在“我的数据 → 我的训练结果”浏览与管理。"
        output
        :container-path="mountPaths.output"
        env-variable="PLATFORM_OUTPUT_PATH"
      />
    </div>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import StorageDirectoryPicker from '../StorageDirectoryPicker.vue'
import DataSpacePicker from '../DataSpacePicker.vue'
import DatasetVersionPicker from '../DatasetVersionPicker.vue'

const props = defineProps({
  form: { type: Object, required: true },
  mountPaths: { type: Object, required: true },
})
const useDataSpaces = ref(Boolean(props.form.input?.spaceId || props.form.checkpoint?.spaceId))
watch(useDataSpaces, () => {
  props.form.input = {}
  props.form.checkpoint = {}
  props.form.output = {}
  props.form.datasetStorage = {}
  props.form.checkpointStorage = {}
  props.form.outputStorage = {}
})
</script>
