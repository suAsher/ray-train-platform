<template>
  <div class="max-w-4xl mx-auto space-y-6">
    <!-- Dev Workspace Banner -->
    <div class="bg-[#131826] p-8 rounded-2xl border border-slate-800/80 shadow-2xl space-y-6">
      <div class="flex justify-between items-start">
        <div>
          <h3 class="text-lg font-bold text-white flex items-center gap-2">
            <el-icon class="text-amber-400"><Platform /></el-icon> 单卡 4090 交互调试工作台 (Interactive Dev Studio)
          </h3>
          <p class="text-xs text-slate-400 mt-1">在单张 RTX 4090 上快速调通代码、验证 Loss 与模型逻辑；训练前请准备一个已写入 IDC 的不可变快照 ID。</p>
        </div>

        <el-tag type="success">多租户隔离: Keycloak 当前租户</el-tag>
      </div>

      <el-form label-width="140px" label-position="left">
        <el-form-item label="登录用户">
          <el-input value="由 Keycloak 当前会话确定" disabled />
        </el-form-item>

        <el-form-item label="单卡调试资源">
          <span class="text-xs font-mono text-emerald-400 font-bold">1x RTX 4090 (24GB VRAM) + IDC PVC 共享挂载</span>
        </el-form-item>

        <el-form-item label="训练快照 ID">
          <el-input v-model="snapshotId" placeholder="可选：IDC 中已准备好的不可变快照目录名" />
        </el-form-item>

        <el-form-item>
          <el-button type="warning" icon="VideoPlay" class="!rounded-xl shadow-lg shadow-amber-600/20" @click="launchDev">
            启动单卡 4090 调试环境
          </el-button>
        </el-form-item>
      </el-form>
    </div>

    <!-- Active Dev Workspace Status -->
    <div v-if="workspace" class="bg-[#131826] border border-amber-500/40 p-6 rounded-2xl space-y-5 shadow-2xl relative overflow-hidden">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <span class="w-3 h-3 rounded-full bg-emerald-400 animate-pulse"></span>
          <span class="text-sm font-bold text-white font-mono">调试环境: {{ workspace.id }} · {{ workspace.status }}</span>
        </div>
        <el-tag type="warning" size="small">1x 4090 在用</el-tag>
      </div>

      <div class="p-4 bg-slate-950/80 rounded-xl border border-slate-800 text-xs font-mono space-y-2 text-slate-300">
        <div>IDC 快照: <span class="text-amber-400 font-bold">{{ workspace.snapshot_id || '未指定' }}</span></div>
        <div>Jupyter 访问由平台 API 代理；代码目录和启动命令由用户在工作区内确认。</div>
      </div>

      <!-- Actions: Open Jupyter OR Conversion to Distributed Job -->
      <div class="flex items-center justify-between pt-2">
        <a :href="workspace.jupyter_url" target="_blank">
          <el-button type="warning" icon="Link" class="!rounded-xl">打开在线 JupyterLab Web 调试界面</el-button>
        </a>
        <el-button type="danger" plain icon="CircleClose" @click="stopDev">停止工作区</el-button>

        <!-- THE KEY ONE-CLICK CONVERSION BUTTON -->
        <el-button type="primary" icon="Zap" class="!rounded-xl shadow-lg shadow-blue-600/30" @click="convertJob">
          🚀 使用快照提交 24 卡分布式训练
        </el-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { apiGet, apiPost, apiDelete } from '../../api/client'

const router = useRouter()
const workspace = ref(null)
const snapshotId = ref('')

const normalizeWorkspace = (value) => ({
  ...value,
  jupyter_url: value.jupyterUrl || value.jupyter_url,
  snapshot_id: value.snapshotId || value.snapshot_id,
  gpu_count: value.gpuCount || value.gpu_count,
  status: value.state || value.status
})

const launchDev = async () => {
  try {
    workspace.value = normalizeWorkspace(await apiPost('/api/v1/dev-workspaces', { name: 'interactive-dev', snapshotId: snapshotId.value }))
    ElMessage.success('已提交单卡 4090 调试工作区创建请求')
  } catch (error) {
    ElMessage.error(error.message || '启动调试工作区失败')
  }
}

const stopDev = async () => {
  try {
    await apiDelete('/api/v1/dev-workspaces/me')
    workspace.value = null
    ElMessage.success('调试工作区已提交停止')
  } catch (error) {
    ElMessage.error(error.message || '停止调试工作区失败')
  }
}

const convertJob = () => {
  if (!workspace.value?.snapshot_id) {
    ElMessage.warning('当前工作区没有快照 ID；请先在 IDC 中准备不可变快照，再从任务页选择工作区快照')
    return
  }
  ElMessage.success('已带入工作区快照，跳转至分布式任务提交页')
  router.push({
    path: '/job/create',
    query: {
      from: 'dev_workspace',
      snapshot: workspace.value.snapshot_id
    }
  })
}

apiGet('/api/v1/dev-workspaces/me')
  .then(value => { workspace.value = normalizeWorkspace(value) })
  .catch(() => {})
</script>
