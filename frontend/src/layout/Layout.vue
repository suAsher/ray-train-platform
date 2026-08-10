<template>
  <div class="flex h-screen bg-[#0B0E17] text-slate-100 font-sans overflow-hidden select-none">
    <!-- Left Navigation Sidebar -->
    <aside class="w-64 border-r border-slate-800/80 bg-[#0F131F] flex flex-col justify-between">
      <div class="min-h-0 flex flex-col">
        <!-- Brand Header -->
        <div class="p-5 border-b border-slate-800/80 flex items-center gap-3">
          <div class="p-2.5 bg-gradient-to-tr from-blue-600 to-indigo-500 rounded-xl text-white shadow-lg shadow-blue-600/30">
            <el-icon :size="20"><Zap /></el-icon>
          </div>
          <div>
            <h1 class="font-bold text-base text-white tracking-wide">Ray AI Platform</h1>
            <p class="text-xs text-slate-400">分布式训练控制台</p>
          </div>
        </div>

        <nav class="p-3 space-y-4 overflow-y-auto">
          <!-- Everyday workspace: visible to every signed-in engineer -->
          <div class="space-y-1">
            <p class="px-4 pt-1 pb-1 text-[11px] font-semibold uppercase tracking-wider text-slate-600">工作台</p>
            <router-link v-for="item in workspaceNav" :key="item.to" :to="item.to" :class="navClass" :active-class="navActiveClass">
              <el-icon><component :is="item.icon" /></el-icon> {{ item.label }}
            </router-link>
          </div>

          <!-- Platform administration: hidden unless the server says so -->
          <div v-if="isAdmin" class="space-y-1">
            <p class="px-4 pt-1 pb-1 text-[11px] font-semibold uppercase tracking-wider text-slate-600">平台管理</p>
            <router-link v-for="item in adminNav" :key="item.to" :to="item.to" :class="navClass" :active-class="navActiveClass">
              <el-icon><component :is="item.icon" /></el-icon> {{ item.label }}
            </router-link>
          </div>
        </nav>
      </div>

      <!-- The user's own GPU budget, straight from the quota the API enforces -->
      <div class="p-4 m-3 bg-[#131826] rounded-2xl border border-slate-800/80 space-y-3 shadow-xl">
        <div class="flex items-center justify-between text-xs">
          <span class="font-semibold text-slate-300">我的 GPU 配额</span>
          <el-tag size="small" :type="quotaTagType">{{ quotaLabel }}</el-tag>
        </div>
        <template v-if="quota">
          <div class="flex justify-between text-xs font-mono">
            <span class="text-slate-400">已占用</span>
            <span class="text-slate-200 font-bold">{{ quota.gpuUsed }} / {{ quota.gpuLimit }} 卡</span>
          </div>
          <el-progress :percentage="quotaPercent" :show-text="false" :status="quotaProgressStatus" />
          <p class="text-[11px] text-slate-500">还可申请 {{ quota.gpuAvailable }} 卡</p>
        </template>
        <p v-else class="text-[11px] text-slate-500">配额信息暂不可用</p>
      </div>
    </aside>

    <!-- Main Right Content Area -->
    <main class="flex-1 flex flex-col min-w-0 bg-[#0B0E17]">
      <header class="h-16 border-b border-slate-800/80 px-8 flex items-center justify-between bg-[#0F131F]/50 backdrop-blur-md">
        <div class="flex items-center gap-4">
          <h2 class="text-base font-bold text-white tracking-wide">{{ currentTitle }}</h2>

          <div class="flex items-center gap-2 bg-slate-900 px-3 py-1.5 rounded-xl border border-slate-800">
            <span class="text-xs text-slate-400">团队:</span>
            <span class="text-slate-200 font-mono text-xs">{{ tenantLabel }}</span>
          </div>

          <div
            v-if="isDemoSession"
            class="px-3 py-1.5 rounded-lg bg-amber-950/40 border border-amber-800/50 text-amber-300 text-xs font-mono"
          >
            本地调试模式 · 未启用 SSO
          </div>
        </div>

        <div class="flex items-center gap-4 text-xs font-mono">
          <el-dropdown trigger="click" @command="onUserCommand">
            <div class="flex items-center gap-2 text-slate-300 cursor-pointer hover:text-white transition-colors">
              <el-avatar :size="28">{{ displayName.slice(0, 1).toUpperCase() }}</el-avatar>
              <span class="font-semibold">{{ displayName }}</span>
              <el-icon><ArrowDown /></el-icon>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item disabled>{{ roleLabel }}</el-dropdown-item>
                <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </header>

      <div class="flex-1 p-8 overflow-y-auto">
        <router-view />
      </div>
    </main>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ArrowDown, VideoPlay, Platform, FolderOpened, Lock, DataAnalysis, Monitor } from '@element-plus/icons-vue'
import { logout } from '../auth'
import { fetchMyQuota } from '../api/quota'
import { session, isAdmin, isDemoSession, roles } from '../stores/session'

const REFRESH_INTERVAL_MS = 15000

const route = useRoute()
const quota = ref(null)
let quotaTimer

const workspaceNav = [
  { to: '/job', label: '我的训练任务', icon: VideoPlay },
  { to: '/devcenter', label: '交互式调试', icon: Platform },
  { to: '/datacache', label: '数据与模型产物', icon: FolderOpened }
]

const adminNav = [
  { to: '/quota', label: '租户与配额', icon: Lock },
  { to: '/control-center', label: '集群算力', icon: DataAnalysis },
  { to: '/devices', label: 'GPU 节点', icon: Monitor }
]

const navClass = 'flex items-center gap-3 px-4 py-2.5 rounded-xl text-sm font-medium transition-all text-slate-400 hover:bg-slate-800/50 hover:text-slate-200'
const navActiveClass = 'bg-blue-600/15 !text-blue-400 border border-blue-500/30 font-bold shadow-inner'

async function refreshQuota() {
  try {
    quota.value = await fetchMyQuota()
  } catch {
    quota.value = null
  }
}

onMounted(() => {
  refreshQuota()
  quotaTimer = window.setInterval(refreshQuota, REFRESH_INTERVAL_MS)
})
onUnmounted(() => window.clearInterval(quotaTimer))

const displayName = computed(() => session.value?.username || (isDemoSession.value ? '本地调试用户' : '加载中…'))
const tenantLabel = computed(() => session.value?.tenantId || '—')
const roleLabel = computed(() => (roles.value.length ? roles.value.join(' / ') : '无角色'))
const currentTitle = computed(() => route.meta.title || 'Ray 分布式训练平台')

const quotaPercent = computed(() => {
  if (!quota.value?.gpuLimit) return 0
  return Math.min(100, Math.round((quota.value.gpuUsed / quota.value.gpuLimit) * 100))
})
const quotaProgressStatus = computed(() => (quotaPercent.value >= 100 ? 'exception' : 'success'))
const quotaTagType = computed(() => (quota.value ? (quotaPercent.value >= 100 ? 'danger' : 'success') : 'info'))
const quotaLabel = computed(() => (quota.value ? (quotaPercent.value >= 100 ? '已满' : '实时') : '不可用'))

function onUserCommand(command) {
  if (command === 'logout') logout()
}
</script>
