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

        <el-tag type="success">多租户隔离：按当前登录租户</el-tag>
      </div>

      <el-form label-width="140px" label-position="left">
        <el-form-item label="登录用户">
          <el-input :value="sessionLabel" disabled />
        </el-form-item>

        <el-form-item label="单卡调试资源">
          <span class="text-xs font-mono text-emerald-400 font-bold">1x RTX 4090 (24GB VRAM) + IDC PVC 共享挂载</span>
        </el-form-item>

        <el-form-item label="调试镜像">
          <el-select v-model="selectedImage" class="w-full" placeholder="选择运行环境" :loading="loadingImages" :disabled="hasActiveWorkspace">
            <el-option v-for="image in images" :key="image.id" :label="image.name + (image.framework ? ` · ${image.framework}` : '')" :value="image.reference">
              <div class="flex justify-between items-center gap-4">
                <span>{{ image.name }}<el-tag v-if="image.isDefault" size="small" type="success" effect="plain" class="ml-2">默认</el-tag></span>
                <span class="text-[11px] text-slate-500">{{ image.framework }}</span>
              </div>
            </el-option>
          </el-select>
          <p v-if="!loadingImages && images.length === 0" class="text-[11px] text-amber-400 mt-1">
            镜像目录为空，将使用部署默认镜像。管理员可在「租户与配额」页登记镜像。
          </p>
        </el-form-item>

        <el-form-item label="训练快照 ID">
          <el-input v-model="snapshotId" placeholder="可选：IDC 中已准备好的不可变快照目录名" :disabled="hasActiveWorkspace" />
        </el-form-item>

        <el-form-item>
          <el-button
            type="warning"
            icon="VideoPlay"
            class="!rounded-xl shadow-lg shadow-amber-600/20"
            :loading="launching"
            :disabled="hasActiveWorkspace"
            @click="launchDev"
          >
            {{ hasActiveWorkspace ? '调试环境已在运行' : '启动单卡 4090 调试环境' }}
          </el-button>
          <span v-if="hasActiveWorkspace" class="text-[11px] text-slate-500 ml-3">
            每位用户同时只能有一个调试环境；如需更换镜像请先停止当前工作区。
          </span>
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
        <el-button type="warning" icon="Link" class="!rounded-xl" :loading="openingEditor === 'jupyter'" :disabled="!isReady" @click="openEditor('jupyter')">
          打开 JupyterLab
        </el-button>
        <el-button type="primary" icon="Monitor" class="!rounded-xl" :loading="openingEditor === 'vscode'" :disabled="!isReady" @click="openEditor('vscode')">
          打开 VS Code
        </el-button>
        <el-tag v-if="!isReady" type="warning" effect="plain" size="small">环境启动中，就绪后可打开</el-tag>
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
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { apiGet, apiPost, apiDelete } from '../../api/client'
import { fetchImages } from '../../api/catalog'
import { session } from '../../stores/session'

const POLL_INTERVAL_MS = 5000
const ACTIVE_STATES = ['SUBMITTED', 'PROVISIONING', 'RUNNING']

const router = useRouter()
const workspace = ref(null)
const images = ref([])
const selectedImage = ref('')
const snapshotId = ref('')
const loadingImages = ref(false)
const launching = ref(false)
const openingEditor = ref('')
let pollTimer

const sessionLabel = computed(() => {
  const user = session.value
  return user ? `${user.username}（租户 ${user.tenantId}）` : '未登录'
})

// A workspace that is starting or running blocks a second launch, which is
// what the API enforces too; without this the button looked like it did
// nothing when clicked repeatedly.
const hasActiveWorkspace = computed(() => ACTIVE_STATES.includes(workspace.value?.status))
const isReady = computed(() => workspace.value?.status === 'RUNNING')

const normalizeWorkspace = (value) => value && ({
  ...value,
  jupyter_url: value.jupyterUrl || value.jupyter_url,
  snapshot_id: value.snapshotId || value.snapshot_id,
  gpu_count: value.gpuCount || value.gpu_count,
  status: value.state || value.status
})

const refreshWorkspace = async () => {
  try {
    workspace.value = normalizeWorkspace(await apiGet('/api/v1/dev-workspaces/me'))
  } catch {
    // A 404 simply means this user has no workspace yet.
    workspace.value = null
  }
}

const loadImages = async () => {
  loadingImages.value = true
  try {
    images.value = await fetchImages('workspace')
    const preferred = images.value.find((image) => image.isDefault) || images.value[0]
    if (preferred && !selectedImage.value) selectedImage.value = preferred.reference
  } catch {
    images.value = []
  } finally {
    loadingImages.value = false
  }
}

onMounted(async () => {
  await Promise.all([refreshWorkspace(), loadImages()])
  // Poll so SUBMITTED -> RUNNING appears without a manual reload.
  pollTimer = window.setInterval(refreshWorkspace, POLL_INTERVAL_MS)
})
onUnmounted(() => window.clearInterval(pollTimer))

const launchDev = async () => {
  if (hasActiveWorkspace.value) return
  launching.value = true
  try {
    workspace.value = normalizeWorkspace(await apiPost('/api/v1/dev-workspaces', {
      name: 'interactive-dev',
      image: selectedImage.value,
      snapshotId: snapshotId.value
    }))
    ElMessage.success('调试环境创建中，就绪后即可打开编辑器')
  } catch (error) {
    ElMessage.error(error.message || '启动调试工作区失败')
  } finally {
    launching.value = false
  }
}

const stopDev = async () => {
  try {
    await apiDelete('/api/v1/dev-workspaces/me')
    workspace.value = null
    ElMessage.success('调试工作区已停止')
  } catch (error) {
    ElMessage.error(error.message || '停止调试工作区失败')
  }
}

/**
 * The editors open in a new tab, and a plain navigation cannot carry the bearer
 * token. Ask the API for a short-lived signed URL; the proxy swaps it for a
 * workspace-scoped cookie so the editor's own asset requests stay authorised.
 */
const openEditor = async (kind) => {
  if (!workspace.value?.id || !isReady.value) return
  openingEditor.value = kind
  try {
    const access = await apiPost(`/api/v1/dev-workspaces/${workspace.value.id}/access`, {})
    const target = kind === 'vscode' ? access.vscodeUrl : access.url
    const separator = target.includes('?') ? '&' : '?'
    window.open(`${target}${separator}subject=${encodeURIComponent(session.value?.subject || '')}`, '_blank', 'noopener')
  } catch (error) {
    ElMessage.error(error.message || '无法打开调试界面')
  } finally {
    openingEditor.value = ''
  }
}

const convertJob = () => {
  if (!workspace.value?.snapshot_id) {
    ElMessage.warning('当前工作区没有快照 ID；请先在 IDC 中准备不可变快照，再从任务页选择工作区快照')
    return
  }
  router.push({
    path: '/job/create',
    query: { from: 'dev_workspace', snapshot: workspace.value.snapshot_id }
  })
}
</script>
