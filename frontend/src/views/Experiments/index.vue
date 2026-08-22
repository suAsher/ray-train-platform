<template>
  <div class="space-y-5">
    <section class="panel flex flex-wrap items-center justify-between gap-4 p-6">
      <div>
        <div class="flex items-center gap-3">
          <div class="rounded-xl bg-emerald-500/10 p-2 text-emerald-400">
            <el-icon :size="20"><TrendCharts /></el-icon>
          </div>
          <div>
            <h3 class="text-base font-bold text-white">实验中心</h3>
            <p class="mt-1 text-xs text-slate-400">长期查看 MLflow 训练记录；任务资源清理后，历史指标仍保留在这里。</p>
          </div>
        </div>
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <el-button
          type="primary"
          :loading="dashboardOpening"
          :disabled="dashboardOpening"
          class="!rounded-xl"
          @click="openMLflowDashboard"
        >打开 MLflow 管理界面</el-button>
        <el-button :loading="loading" icon="Refresh" class="!rounded-xl" @click="loadExperiments">刷新</el-button>
      </div>
    </section>

    <el-alert
      type="warning"
      show-icon
      :closable="false"
      class="!rounded-xl"
    >
      <template #title>
        <span class="text-xs leading-5">
          MLflow 原生界面是全局共享视图：所有已登录平台用户都可以查看和变更共享实验；创建、修改、删除、模型注册表以及 MLflow Artifact 上传和下载均已启用。这不会开放公开训练数据下载。
        </span>
      </template>
    </el-alert>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <div v-for="card in summaryCards" :key="card.label" class="stat-tile panel-hover">
        <p class="stat-tile__label">{{ card.label }}</p>
        <p class="stat-tile__value" :class="card.tone">{{ card.value }}</p>
        <p class="text-[11px] text-slate-500">{{ card.hint }}</p>
      </div>
    </div>

    <el-alert
      v-if="errorMessage"
      :title="errorMessage"
      type="error"
      show-icon
      :closable="false"
      class="!rounded-xl"
    >
      <template #default>
        <el-button link type="primary" @click="loadExperiments">重新加载</el-button>
      </template>
    </el-alert>

    <section class="panel overflow-hidden">
      <el-table :data="experiments" class="!bg-transparent text-xs" style="width: 100%" v-loading="loading">
        <template #empty>
          <div class="py-12 space-y-2">
            <p class="font-semibold text-slate-300">{{ errorMessage ? '实验记录暂不可用' : '还没有实验记录' }}</p>
            <p class="text-xs text-slate-500">训练任务开始向 MLflow 上报后，Run 会出现在这里。</p>
          </div>
        </template>

        <el-table-column label="Run" min-width="220">
          <template #default="scope">
            <div class="font-mono font-bold text-slate-100">{{ scope.row.runName || '未命名 Run' }}</div>
            <code class="mt-1 block truncate text-[11px] text-slate-500">{{ scope.row.runId || '—' }}</code>
            <span v-if="scope.row.experimentName" class="mt-1 block truncate text-[11px] text-slate-600">
              {{ scope.row.experimentName }}
            </span>
          </template>
        </el-table-column>

        <el-table-column label="训练任务" min-width="210">
          <template #default="scope">
            <router-link
              v-if="scope.row.jobId"
              :to="{ name: 'JobDetail', params: { id: scope.row.jobId } }"
              class="font-semibold text-blue-400 transition-colors hover:text-blue-300"
            >
              {{ scope.row.jobName || scope.row.jobId }}
            </router-link>
            <span v-else class="text-slate-500">—</span>
            <code v-if="scope.row.jobId" class="mt-1 block truncate text-[11px] text-slate-500">{{ scope.row.jobId }}</code>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="110">
          <template #default="scope">
            <el-tag :type="statusPresentation(scope.row.status).type" size="small">
              {{ statusPresentation(scope.row.status).label }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column label="开始时间" width="185">
          <template #default="scope">
            <span class="font-mono text-[11px] text-slate-400">{{ formatExperimentTime(scope.row.startTimeMs) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="结束时间" width="185">
          <template #default="scope">
            <span class="font-mono text-[11px] text-slate-400">{{ formatExperimentTime(scope.row.endTimeMs) }}</span>
          </template>
        </el-table-column>

        <el-table-column label="时长" width="120">
          <template #default="scope">
            <span class="font-mono text-[11px] text-slate-300">
              {{ formatExperimentDuration(scope.row.startTimeMs, scope.row.endTimeMs, clockMs) }}
            </span>
          </template>
        </el-table-column>

        <el-table-column label="最新关键指标" min-width="330">
          <template #default="scope">
            <div v-if="scope.row.metrics.length" class="flex flex-wrap gap-2">
              <span
                v-for="metric in scope.row.metrics"
                :key="metric.key"
                class="rounded-lg border border-slate-700 bg-slate-900/70 px-2 py-1 font-mono text-[11px]"
              >
                <span class="text-slate-500">{{ metric.label }}</span>
                <span class="ml-1 font-bold text-emerald-400">{{ formatMetricValue(metric.value) }}</span>
              </span>
            </div>
            <span v-else class="text-slate-500">尚未上报</span>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </div>
</template>

<script setup>
import { ElMessage } from 'element-plus'
import { computed, onMounted, onUnmounted, ref } from 'vue'

import { requestMLflowDashboardAccess } from '../../api/mlflowDashboard.js'
import {
  fetchExperimentCatalog,
  formatExperimentDuration,
  formatExperimentTime,
  formatMetricValue,
  statusPresentation,
} from '../../experimentCatalog.js'

const CLOCK_REFRESH_MS = 30_000

const experiments = ref([])
const loading = ref(false)
const dashboardOpening = ref(false)
const errorMessage = ref('')
const clockMs = ref(Date.now())
let clockTimer

const summaryCards = computed(() => {
  const statusCount = (status) => experiments.value.filter((item) => item.status.toUpperCase() === status).length
  return [
    { label: '全部 Run', value: experiments.value.length, hint: '当前账号可见', tone: 'text-white' },
    { label: '运行中', value: statusCount('RUNNING'), hint: '正在持续上报', tone: 'text-emerald-400' },
    { label: '已完成', value: statusCount('FINISHED'), hint: '长期保留记录', tone: 'text-blue-400' },
    { label: '失败 / 终止', value: statusCount('FAILED') + statusCount('KILLED'), hint: '可回到任务详情排查', tone: 'text-rose-400' },
  ]
})

async function loadExperiments() {
  loading.value = true
  errorMessage.value = ''
  try {
    experiments.value = await fetchExperimentCatalog()
  } catch (error) {
    experiments.value = []
    errorMessage.value = error?.message || '无法读取实验记录'
  } finally {
    loading.value = false
  }
}

async function openMLflowDashboard() {
  if (dashboardOpening.value) return

  const popup = window.open('about:blank', '_blank', 'noopener,noreferrer')
  if (!popup) {
    ElMessage.warning('浏览器阻止了新标签页，请允许本站打开新标签页后重试')
    return
  }
  if ('opener' in popup) popup.opener = null

  dashboardOpening.value = true
  try {
    const accessURL = await requestMLflowDashboardAccess()
    popup.location.replace(accessURL)
  } catch (error) {
    popup.close()
    ElMessage.error(error?.message || '无法打开 MLflow 管理界面')
  } finally {
    dashboardOpening.value = false
  }
}

onMounted(() => {
  loadExperiments()
  clockTimer = window.setInterval(() => {
    clockMs.value = Date.now()
  }, CLOCK_REFRESH_MS)
})

onUnmounted(() => window.clearInterval(clockTimer))
</script>
