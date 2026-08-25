<template>
  <div class="max-w-4xl mx-auto space-y-6">
    <!-- Dev Workspace Banner -->
    <div class="bg-[#131826] p-8 rounded-2xl border border-slate-800/80 shadow-2xl space-y-6">
      <div class="flex justify-between items-start">
        <div>
          <h3 class="text-lg font-bold text-white flex items-center gap-2">
            <el-icon class="text-amber-400"><Platform /></el-icon> GPU 交互调试工作台
          </h3>
          <p class="text-xs text-slate-400 mt-1">在 GPU Worker 上快速调通代码和数据路径；无需填写对象存储地址或密钥。</p>
        </div>

        <el-tag type="success">多租户隔离：按当前登录租户</el-tag>
      </div>

      <el-form label-width="140px" label-position="left">
        <el-form-item label="登录用户">
          <el-input :value="sessionLabel" disabled />
        </el-form-item>

        <el-form-item label="调试资源">
          <div class="space-y-2 w-full">
            <el-radio-group v-model="selectedGPUCount" :disabled="hasActiveWorkspace" class="flex flex-wrap gap-2">
              <el-radio-button v-for="profile in workspaceProfiles" :key="profile.id" :label="profile.gpuCount">
                {{ profile.label }}
              </el-radio-button>
            </el-radio-group>
            <p class="text-xs font-mono font-bold" :class="storageReady ? 'text-emerald-400' : 'text-amber-400'">{{ selectedWorkspaceProfile.topology }} · {{ storageReady ? '授权数据空间将自动挂载' : '当前为临时工作目录，受管数据挂载待管理员验收' }}</p>
            <p class="text-[11px] text-slate-500">{{ selectedWorkspaceProfile.description }} 多机调试请直接提交“多机多卡 Ray Train”验证任务，避免编辑器连接到不确定的 worker。</p>
          </div>
        </el-form-item>

        <el-form-item label="调试镜像">
          <el-select v-model="selectedImage" class="w-full" placeholder="选择运行环境" :loading="loadingImages" :disabled="hasActiveWorkspace">
            <el-option v-for="image in images" :key="image.id" :label="imageLabel(image)" :value="image.reference">
              <div class="flex justify-between items-center gap-4">
                <span>{{ image.name }}<el-tag v-if="image.isDefault" size="small" type="success" effect="plain" class="ml-2">默认</el-tag></span>
                <span class="text-[11px] text-slate-500">{{ image.framework }}</span>
              </div>
            </el-option>
          </el-select>
          <p v-if="!loadingImages && images.length === 0" class="text-[11px] text-amber-400 mt-1">
            镜像目录为空，暂不能启动调试环境。请由团队管理员在「平台管理」页登记调试镜像。
          </p>
        </el-form-item>

        <el-form-item>
          <el-button
            type="warning"
            icon="VideoPlay"
            class="!rounded-xl shadow-lg shadow-amber-600/20"
            :loading="launching"
            :disabled="hasActiveWorkspace || (!loadingImages && images.length === 0)"
            @click="launchDev"
          >
            {{ hasActiveWorkspace ? '调试环境已在运行' : `启动 ${selectedWorkspaceProfile.topology} 调试环境` }}
          </el-button>
          <span v-if="hasActiveWorkspace" class="text-[11px] text-slate-500 ml-3">
            每位用户同时只能有一个调试环境；如需更换镜像请先停止当前工作区。
          </span>
        </el-form-item>
      </el-form>
    </div>

    <el-alert v-if="storageStatusAlert" :type="storageStatusAlert.type" :closable="false" class="!rounded-xl">
      <template #title>{{ storageStatusAlert.message }}</template>
    </el-alert>

    <!-- Active Dev Workspace Status -->
      <div v-if="workspace" class="bg-[#131826] border border-amber-500/40 p-6 rounded-2xl space-y-5 shadow-2xl relative overflow-hidden">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <span class="w-3 h-3 rounded-full" :class="workspaceStatus.dotClass"></span>
          <span class="text-sm font-bold text-white font-mono">调试环境: {{ workspace.id }} · {{ workspaceStatus.label }}</span>
        </div>
        <div class="flex items-center gap-2">
          <span class="text-[11px] text-slate-500">同步于 {{ lastSyncLabel }}</span>
          <el-button text size="small" :loading="refreshing" @click="refreshWorkspace(true)">刷新状态</el-button>
          <el-tag :type="workspaceStatus.tagType" size="small">{{ workspaceStatus.resourceLabel }}</el-tag>
        </div>
      </div>

      <div class="p-4 bg-slate-950/80 rounded-xl border border-slate-800 text-xs font-mono space-y-2 text-slate-300">
        <div>Jupyter 和 VS Code 均连接 GPU Worker；Ray Head 不提供交互入口。</div>
        <template v-if="storageReady">
          <div>个人工作区: <span class="text-emerald-300">/workspace</span>（可写） · 个人文件: <span class="text-emerald-300">/mnt/storage/me</span>（可写） · 训练结果: <span class="text-emerald-300">/mnt/storage/me/runs</span>（可写）</div>
          <div>团队/公共数据: <span class="text-sky-300">/mnt/storage/team</span>、<span class="text-sky-300">/mnt/storage/public</span>（只读）</div>
          <div>IDC 数据: <span class="text-sky-300">/mnt/idc/original</span>、<span class="text-sky-300">/mnt/idc/wellspiking</span>、<span class="text-sky-300">/mnt/idc/shared</span>（管理员登记后只读）</div>
          <div>Python 依赖: <span class="text-amber-200">cd /workspace &amp;&amp; python -m venv .venv &amp;&amp; . .venv/bin/activate &amp;&amp; pip install -r requirements.txt</span></div>
          <div>系统级 apt 依赖: 请在“调试镜像”下拉中选择团队发布的自定义 Harbor 镜像；停止工作区后通过 apt 安装的内容不会保留。</div>
        </template>
        <div v-else>当前环境不声明 <span class="text-slate-200">/workspace</span>、<span class="text-slate-200">/mnt/storage/*</span> 或 <span class="text-slate-200">/mnt/idc/*</span> 的持久数据可用性；临时代码请放在 <span class="text-slate-200">~/workspace</span>，并通过 <span class="text-amber-200">python -m venv ~/workspace/.venv</span> 安装 Python 依赖。停止工作区后这些内容会丢失。</div>
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
        <el-button v-if="hasActiveWorkspace" type="danger" plain icon="CircleClose" @click="stopDev">停止工作区</el-button>

        <!-- THE KEY ONE-CLICK CONVERSION BUTTON -->
        <el-button type="primary" icon="Zap" class="!rounded-xl shadow-lg shadow-blue-600/30" :loading="convertingJob" :disabled="!isReady" @click="convertJob">
          🚀 创建代码版本并提交分布式训练
        </el-button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { createWorkspaceSnapshot } from '../../api/dataSpaces'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { apiGet, apiPost, apiDelete } from '../../api/client'
import { fetchImages } from '../../api/catalog'
import { fetchDataSpaces } from '../../api/dataSpaces'
import { dataSpaceReadiness } from '../../dataSpaceReadiness'
import { session } from '../../stores/session'
import { interactiveWorkspaceProfiles, workspaceProfileForGPUCount } from '../../devWorkspaceProfiles'

const POLL_INTERVAL_MS = 5000
const ACTIVE_STATES = ['SUBMITTED', 'PROVISIONING', 'RUNNING']

const router = useRouter()
const workspace = ref(null)
const images = ref([])
const selectedImage = ref('')
const selectedGPUCount = ref(1)
const loadingImages = ref(false)
const storageLoaded = ref(false)
const dataSpaces = ref([])
const launching = ref(false)
const openingEditor = ref('')
const refreshing = ref(false)
const lastSyncedAt = ref(null)
let pollTimer

const sessionLabel = computed(() => {
  const user = session.value
  return user ? `${user.username}（租户 ${user.tenantId}）` : '未登录'
})
const workspaceProfiles = interactiveWorkspaceProfiles
const selectedWorkspaceProfile = computed(() => workspaceProfileForGPUCount(selectedGPUCount.value))

// A workspace that is starting or running blocks a second launch, which is
// what the API enforces too; without this the button looked like it did
// nothing when clicked repeatedly.
const hasActiveWorkspace = computed(() => ACTIVE_STATES.includes(workspace.value?.status))
const isReady = computed(() => workspace.value?.status === 'RUNNING')
const workspaceStatus = computed(() => {
  const status = workspace.value?.status || 'SUBMITTED'
  const states = {
    SUBMITTED: { label: '提交中', tagType: 'warning', resourceLabel: '等待创建', dotClass: 'bg-amber-400 animate-pulse' },
    PROVISIONING: { label: '启动中', tagType: 'warning', resourceLabel: '等待资源', dotClass: 'bg-amber-400 animate-pulse' },
    RUNNING: { label: '运行中', tagType: 'success', resourceLabel: `${workspace.value?.gpu_count || 1}x 4090 在用`, dotClass: 'bg-emerald-400 animate-pulse' },
    STOPPED: { label: '已停止', tagType: 'info', resourceLabel: 'GPU 已释放', dotClass: 'bg-slate-500' },
    FAILED: { label: '启动失败', tagType: 'danger', resourceLabel: 'GPU 未占用', dotClass: 'bg-rose-400' }
  }
  return states[status] || { label: status, tagType: 'info', resourceLabel: '状态待确认', dotClass: 'bg-slate-500' }
})
const lastSyncLabel = computed(() => lastSyncedAt.value ? lastSyncedAt.value.toLocaleTimeString() : '—')
const storageReady = computed(() => dataSpaces.value.some((space) => space.id === 'workspace' && dataSpaceReadiness(space).ready))
const storageStatusAlert = computed(() => {
  if (!storageLoaded.value || storageReady.value) return null
  const mountFailed = dataSpaces.value.some((space) => space.provider === 'tos' && space.mountStatus === 'failed')
  if (mountFailed) {
    return {
      type: 'error',
      message: '受控数据目录挂载失败：当前不会创建仅含临时目录的调试环境。请刷新“我的数据”查看失败状态，或联系管理员处理后重试。'
    }
  }
  return {
    type: 'warning',
    message: '受控数据目录正在准备中：个人工作区、个人文件和团队只读目录将在挂载就绪后自动出现在 GPU Worker 中。平台不会把 TOS 地址或密钥暴露给用户。'
  }
})

const normalizeWorkspace = (value) => value && ({
  ...value,
  jupyter_url: value.jupyterUrl || value.jupyter_url,
  snapshot_id: value.snapshotId || value.snapshot_id,
  gpu_count: value.gpuCount || value.gpu_count || 1,
  status: value.state || value.status
})

const refreshWorkspace = async (manual = false) => {
  if (manual) refreshing.value = true
  try {
    workspace.value = normalizeWorkspace(await apiGet('/api/v1/dev-workspaces/me'))
  } catch {
    // A 404 simply means this user has no workspace yet.
    workspace.value = null
  } finally {
    lastSyncedAt.value = new Date()
    if (manual) refreshing.value = false
  }
}


// The default marker belongs in the label: el-select only renders the option
// slot while the dropdown is open, so a tag inside it is invisible once a
// value is chosen.
const imageLabel = (image) => {
  const parts = [image.name]
  if (image.framework) parts.push(image.framework)
  const suffix = image.isDefault ? '（默认）' : ''
  return `${parts.join(' · ')}${suffix}`
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

const loadStorageReadiness = async () => {
  try {
    dataSpaces.value = await fetchDataSpaces()
  } catch {
    dataSpaces.value = []
  } finally {
    storageLoaded.value = true
  }
}

onMounted(async () => {
  await Promise.all([refreshWorkspace(), loadImages(), loadStorageReadiness()])
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
      gpuCount: selectedGPUCount.value,
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

// One click means one click: snapshot the workspace into an immutable code
// version and land on the submit form with it already selected. It previously
// redirected to the data page with an instruction, which is a handoff rather
// than a conversion.
const convertingJob = ref(false)

const convertJob = async () => {
  if (!isReady.value) {
    ElMessage.warning('调试环境尚未就绪，就绪后才能固化当前代码')
    return
  }
  convertingJob.value = true
  try {
    const snapshot = await createWorkspaceSnapshot('')
    ElMessage.success(`已固化当前工作区代码（${snapshot.fileCount || 0} 个文件）`)
    router.push({
      path: '/job/create',
      query: { from: 'dev_workspace', snapshot: snapshot.id },
    })
  } catch (error) {
    ElMessage.error(error.message || '固化工作区代码失败，请稍后重试')
  } finally {
    convertingJob.value = false
  }
}
</script>
