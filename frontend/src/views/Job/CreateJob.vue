<template>
  <div class="max-w-5xl mx-auto space-y-6">
    <!-- Header with Sleek Title -->
    <div class="flex items-center justify-between">
      <div>
        <h3 class="text-xl font-bold text-white flex items-center gap-2">
          <el-icon class="text-blue-500"><VideoPlay /></el-icon> 提交分布式训练任务 (Ray Distributed Training)
        </h3>
        <p class="text-xs text-slate-400 mt-1">从单卡 4090 调试无缝平滑扩容至 24 卡多节点分布式并行训练。</p>
      </div>
      <router-link to="/job">
        <el-button size="small" icon="Back">返回任务列表</el-button>
      </router-link>
    </div>

    <!-- Dev Workspace Auto-Converted Banner -->
    <div v-if="fromDevWorkspace" class="p-4 bg-emerald-950/40 border border-emerald-500/50 rounded-2xl flex items-center justify-between font-mono text-xs shadow-xl">
      <div class="flex items-center gap-3">
        <el-icon class="text-emerald-400" :size="20"><Zap /></el-icon>
        <div>
          <span class="font-bold text-emerald-300">已带入单卡调试工作区快照</span>
          <p class="text-slate-400 mt-0.5">训练代码将由集群 initContainer 从 IDC 快照物化到 <code class="text-white">/workspace</code></p>
        </div>
      </div>
      <el-tag type="success">快照来源已锁定</el-tag>
    </div>

    <!-- PRESET TEMPLATE SELECTION CARDS -->
    <div class="space-y-3">
      <span class="text-xs font-bold text-slate-300 uppercase tracking-wider">或选择一键加载主流模型训练预设 (Preset Templates):</span>
      <div class="grid grid-cols-4 gap-4">
        <div 
          v-for="tpl in presetTemplates" 
          :key="tpl.id"
          @click="applyPreset(tpl)"
          class="p-4 rounded-xl border bg-[#131826] hover:bg-slate-800/80 border-slate-800/80 hover:border-blue-500/50 transition-all cursor-pointer space-y-2 group shadow-lg"
        >
          <div class="flex items-center justify-between">
            <span class="text-xs font-bold text-white group-hover:text-blue-400 font-mono">{{ tpl.name }}</span>
            <el-tag size="small" effect="dark" :type="tpl.tagType">{{ tpl.tag }}</el-tag>
          </div>
          <p class="text-[11px] text-slate-400 line-clamp-2 leading-relaxed">{{ tpl.desc }}</p>
          <div class="text-[10px] font-mono text-emerald-400 font-semibold pt-1">
            推荐: {{ tpl.gpus }} 卡 4090 并行
          </div>
        </div>
      </div>
    </div>

    <!-- Main Wizard Form -->
    <el-form :model="form" label-width="160px" label-position="left" class="bg-[#131826] p-8 rounded-2xl border border-slate-800/80 shadow-2xl space-y-6">
      
      <!-- STEP 1: BASIC & CODE -->
      <div>
        <h4 class="text-xs font-bold text-blue-400 uppercase tracking-wider mb-4 pb-2 border-b border-slate-800 flex items-center gap-2">
          <el-icon><FolderOpened /></el-icon> 1. 代码来源与数据集配置
        </h4>

        <div class="space-y-4">
          <el-form-item label="任务名称 (Job Name)">
            <el-input v-model="form.name" placeholder="例: llama3-8b-instruct-sft" />
          </el-form-item>

          <el-form-item label="训练镜像 digest">
            <el-select v-model="form.image" class="w-full" placeholder="选择训练运行环境" :loading="loadingImages" filterable allow-create>
              <el-option v-for="image in trainingImages" :key="image.id" :label="image.name + (image.framework ? ` · ${image.framework}` : '')" :value="image.reference">
                <div class="flex justify-between items-center gap-4">
                  <span>{{ image.name }}<el-tag v-if="image.isDefault" size="small" type="success" effect="plain" class="ml-2">默认</el-tag></span>
                  <span class="text-[11px] text-slate-500">{{ image.framework }}</span>
                </div>
              </el-option>
            </el-select>
            <p class="text-[11px] text-slate-500 mt-1">
              {{ trainingImages.length ? '从管理员登记的镜像目录中选择，确保依赖环境一致；也可直接粘贴带 digest 的镜像。' : '镜像目录为空，请粘贴带 @sha256 digest 的镜像，或由管理员先登记镜像。' }}
            </p>
            <div class="text-[11px] text-slate-500 mt-1">生产环境只允许不可变的 sha256 镜像，不接受 latest。</div>
          </el-form-item>

          <el-form-item label="代码来源模式">
            <el-radio-group v-model="form.code_source_type">
              <el-radio-button label="workspace">IDC PVC 工作区快照</el-radio-button>
              <el-radio-button label="git">Git 远程仓库</el-radio-button>
              <el-radio-button label="tos">TOS 对象存储 ZIP</el-radio-button>
            </el-radio-group>
          </el-form-item>

          <template v-if="form.code_source_type === 'git'">
            <el-form-item label="Git 仓库 URL">
              <el-input v-model="form.git_url" placeholder="https://github.com/..." />
            </el-form-item>
            <el-form-item label="Git Commit (必须固定)">
              <el-input v-model="form.git_commit" placeholder="40 位 commit SHA" style="width: 360px" />
            </el-form-item>
          </template>

          <template v-else-if="form.code_source_type === 'workspace'">
            <el-form-item label="工作区快照 ID">
              <el-input v-model="form.workspace_snapshot" placeholder="由调试工作区生成的不可变快照 ID" />
            </el-form-item>
          </template>

          <template v-else-if="form.code_source_type === 'tos'">
            <el-form-item label="TOS 代码 URI">
              <el-input v-model="form.tos_code_path" placeholder="tos://bucket/code/release.tar.gz" />
            </el-form-item>
          </template>

          <el-form-item label="TOS 数据集 URI">
            <el-input v-model="form.dataset_path" placeholder="tos://ai-training-data/datasets/sft_v1.json" />
          </el-form-item>
        </div>
      </div>

      <!-- STEP 2: EXECUTION COMMAND -->
      <div>
        <h4 class="text-xs font-bold text-emerald-400 uppercase tracking-wider mb-4 pb-2 border-b border-slate-800 flex items-center gap-2">
          <el-icon><Cpu /></el-icon> 2. 分布式并行启动命令与产物归档
        </h4>

        <div class="space-y-4">
          <el-form-item label="训练框架引擎">
            <el-radio-group v-model="form.framework">
              <el-radio-button label="RayTrain">Ray Train (PyTorch 分布式)</el-radio-button>
              <el-radio-button label="Megatron">Megatron-LM</el-radio-button>
              <el-radio-button label="DeepSpeed">DeepSpeed Zero-3</el-radio-button>
            </el-radio-group>
          </el-form-item>

          <el-form-item label="分布式启动脚本">
            <el-input 
              v-model="form.entrypoint" 
              type="textarea" 
              :rows="3" 
              class="font-mono text-emerald-400 bg-slate-950"
              placeholder="python -m ray.train.torch.run --nnodes 3 --nproc-per-node 8 train.py --batch-size 64" 
            />
          </el-form-item>

          <el-form-item label="Checkpoint 导出 URI">
            <el-input v-model="form.checkpoint_output_dir" placeholder="tos://ai-training-data/checkpoints/run1/" />
          </el-form-item>
        </div>
      </div>

      <!-- STEP 3: PARALLELISM CALCULATOR -->
      <div>
        <h4 class="text-xs font-bold text-purple-400 uppercase tracking-wider mb-4 pb-2 border-b border-slate-800 flex items-center gap-2">
          <el-icon><Setting /></el-icon> 3. 4090 算力规模扩展 (从 1 卡扩展至 24 卡)
        </h4>

        <div class="grid grid-cols-2 gap-6 bg-slate-900/60 p-5 rounded-xl border border-slate-800">
          <el-form-item label="请求节点数量 (Nodes)">
            <el-input-number v-model="form.worker_replicas" :min="1" :max="3" />
            <span class="text-xs text-slate-500 ml-2">台 (每台 8x 4090)</span>
          </el-form-item>

          <el-form-item label="单节点 GPU 数">
            <el-input-number v-model="form.gpus_per_worker" :min="1" :max="8" />
            <span class="text-xs text-slate-500 ml-2">张</span>
          </el-form-item>
        </div>

        <div class="mt-4 p-5 bg-gradient-to-r from-blue-950/40 via-purple-950/30 to-blue-950/40 rounded-xl border border-blue-900/50 space-y-3">
          <div class="flex justify-between items-center text-xs font-mono">
            <span class="text-slate-300 font-bold">算力分布与 Megatron 并行演算:</span>
            <span class="text-blue-400 font-bold text-sm">共计 {{ totalGpus }} 张 RTX 4090 显卡 (576 GB 总显存)</span>
          </div>

          <div class="grid grid-cols-3 gap-4 text-xs font-mono">
            <div class="p-3 bg-slate-950/80 rounded-lg border border-slate-800">
              <div class="text-slate-400">Tensor Parallel (TP):</div>
              <div class="text-blue-400 font-bold text-sm mt-0.5">{{ form.worker_replicas > 1 ? 2 : 1 }}</div>
            </div>
            <div class="p-3 bg-slate-950/80 rounded-lg border border-slate-800">
              <div class="text-slate-400">Pipeline Parallel (PP):</div>
              <div class="text-purple-400 font-bold text-sm mt-0.5">{{ form.worker_replicas > 1 ? 2 : 1 }}</div>
            </div>
            <div class="p-3 bg-slate-950/80 rounded-lg border border-slate-800">
              <div class="text-slate-400">Data Parallel (DP):</div>
              <div class="text-emerald-400 font-bold text-sm mt-0.5">{{ totalGpus / (form.worker_replicas > 1 ? 4 : 1) }}</div>
            </div>
          </div>
        </div>
      </div>

      <!-- SUBMIT ACTION -->
      <div class="pt-6 border-t border-slate-800 flex justify-end gap-4">
        <router-link to="/job">
          <el-button class="!rounded-xl">取消</el-button>
        </router-link>
        <el-button type="primary" icon="Check" class="!rounded-xl shadow-lg shadow-blue-600/30" @click="submitJob">
          一键提交分布式训练任务
        </el-button>
      </div>

    </el-form>
  </div>
</template>

<script setup>
import { reactive, ref, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { apiPost } from '../../api/client'
import { fetchImages } from '../../api/catalog'

const router = useRouter()
const route = useRoute()

const fromDevWorkspace = computed(() => route.query.from === 'dev_workspace')

const presetTemplates = [
  {
    id: 'llama3-8b',
    name: 'Llama-3-8B SFT',
    tag: '大语言模型',
    tagType: 'primary',
    desc: '基于 Ray Train + PyTorch FSDP 对 Llama-3-8B 进行 24 卡全量 SFT 微调。',
    gpus: 24,
    entrypoint: 'python -m ray.train.torch.run --nnodes 3 --nproc-per-node 8 train_llama.py --batch-size 64 --lr 2e-5'
  },
  {
    id: 'deepseek-r1',
    name: 'DeepSeek-R1 Distill',
    tag: '推理强化学习',
    tagType: 'warning',
    desc: 'DeepSeek-R1 蒸馏模型多卡 GRPO / PPO 强化学习对齐训练。',
    gpus: 24,
    entrypoint: 'python -m ray.train.torch.run --nnodes 3 --nproc-per-node 8 train_grpo.py --model_name_or_path DeepSeek-R1-Distill'
  },
  {
    id: 'qwen25-72b',
    name: 'Qwen-2.5-72B LoRA',
    tag: '千亿大模型',
    tagType: 'success',
    desc: 'Qwen-2.5-72B 大模型 24 卡 Megatron TP=2 PP=2 混合并行训练。',
    gpus: 24,
    entrypoint: 'python -m ray.train.torch.run --nnodes 3 --nproc-per-node 8 train_qwen.py --tp 2 --pp 2'
  },
  {
    id: 'flux-1',
    name: 'FLUX.1 Diffusion',
    tag: '多模态图像',
    tagType: 'danger',
    desc: 'FLUX.1 图像生成大模型 24 卡多节点分布式 LoRA 微调。',
    gpus: 24,
    entrypoint: 'python -m ray.train.torch.run --nnodes 3 --nproc-per-node 8 train_flux.py --resolution 1024'
  }
]

const form = reactive({
  name: 'llama3-8b-instruct-sft',
  idempotency_key: '',
  image: '',
  framework: 'RayTrain',
  code_source_type: 'git',
  workspace_snapshot: '',
  tos_code_path: '',
  git_url: 'https://github.com/meta-llama/llama3.git',
  git_commit: '',
  dataset_path: 'tos://ai-training-data/datasets/sft_v1.json',
  entrypoint: 'python -m ray.train.torch.run --nnodes 3 --nproc-per-node 8 train.py --batch-size 64',
  checkpoint_output_dir: 'tos://ai-training-data/checkpoints/run1/',
  worker_replicas: 3,
  gpus_per_worker: 8
})

const totalGpus = computed(() => form.worker_replicas * form.gpus_per_worker)

const applyPreset = (tpl) => {
  const generatedID = globalThis.crypto?.randomUUID?.()
  const suffix = generatedID ? generatedID.slice(0, 8) : String(Date.now()).slice(-8)
  form.name = `${tpl.id}-run-${suffix}`
  form.entrypoint = tpl.entrypoint
  ElMessage.success(`已成功加载预设模板: ${tpl.name}`)
}

const trainingImages = ref([])
const loadingImages = ref(false)

onMounted(async () => {
  loadingImages.value = true
  try {
    trainingImages.value = await fetchImages('training')
    const preferred = trainingImages.value.find((image) => image.isDefault) || trainingImages.value[0]
    if (preferred && !form.image) form.image = preferred.reference
  } catch {
    trainingImages.value = []
  } finally {
    loadingImages.value = false
  }
})

const submitJob = async () => {
  try {
    if (!form.idempotency_key) form.idempotency_key = crypto.randomUUID()
    const data = await apiPost('/api/v1/jobs', { spec: buildSpec() }, { headers: { 'Idempotency-Key': form.idempotency_key } })
    ElMessage.success('分布式训练任务已提交，正在等待 Kueue 调度')
    router.push(`/job/detail/${data.id}`)
  } catch (error) {
    ElMessage.error(error.message || '提交训练任务失败')
  }
}

const parseEntrypoint = (value) => {
  const parts = []
  const matcher = /"([^"\\]*(?:\\.[^"\\]*)*)"|'([^']*)'|([^\s]+)/g
  let match
  while ((match = matcher.exec(value || '')) !== null) parts.push(match[1] ?? match[2] ?? match[3])
  return parts
}

const buildSpec = () => {
  const command = parseEntrypoint(form.entrypoint)
  if (!command.length) throw new Error('请输入训练启动命令')
  const source = form.code_source_type === 'git'
    ? { type: 'git', url: form.git_url, commit: form.git_commit }
    : form.code_source_type === 'tos'
      ? { type: 'tos', uri: form.tos_code_path }
      : { type: 'workspace', snapshot: form.workspace_snapshot }
  return {
    name: form.name,
    image: form.image,
    source,
    entrypoint: { command: [command[0]], args: command.slice(1) },
    resources: { workerReplicas: form.worker_replicas, gpusPerWorker: form.gpus_per_worker, cpuPerWorker: 8, memoryPerWorker: '32Gi' },
    datasetUri: form.dataset_path,
    outputUri: form.checkpoint_output_dir,
    queue: '',
    timeoutSeconds: 0,
    retryPolicy: { maxRetries: 0 }
  }
}

onMounted(() => {
  if (route.query.from === 'dev_workspace') {
    form.code_source_type = 'workspace'
    form.workspace_snapshot = route.query.snapshot || ''
    form.entrypoint = 'python -m ray.train.torch.run --nnodes 3 --nproc-per-node 8 train.py --batch-size 64'
  }
})
</script>
