<template>
  <div>
    <div class="mb-6">
      <h2 class="text-lg font-semibold text-white">代码与训练环境</h2>
      <p class="mt-1 text-sm text-slate-400">代码、镜像和 Commit 都会随任务记录；镜像可使用管理员批准的 tag 或不可变 digest。</p>
    </div>

    <div class="space-y-5">
      <div>
        <label class="field-label">任务名称</label>
        <el-input v-model="form.name" maxlength="63" placeholder="例如：bevfusion-lidar-001" />
        <p class="field-help">小写字母、数字和短横线；提交后用于定位任务与产物。</p>
      </div>

      <div>
        <label class="field-label">训练镜像</label>
        <el-select v-model="form.image" class="w-full" filterable clearable :loading="loading" placeholder="选择管理员登记的训练环境">
          <el-option v-for="image in images" :key="image.id" :label="imageLabel(image)" :value="image.reference">
            <div class="flex items-center justify-between gap-4">
              <span class="font-medium">{{ image.name }}</span>
              <span class="text-xs text-slate-500">{{ image.framework || '通用训练环境' }}</span>
            </div>
          </el-option>
        </el-select>
        <p class="field-help">只显示当前团队可用、管理员已批准的训练环境。正式基线推荐 digest；日常迭代可使用 tag。</p>
        <p v-if="!loading && images.length === 0" class="mt-2 text-xs text-amber-300">还没有可用镜像，请联系团队管理员登记训练环境。</p>
      </div>

      <div>
        <label class="field-label">代码来源</label>
        <el-radio-group v-model="form.codeSourceType" class="grid w-full gap-3 sm:grid-cols-2">
          <el-radio-button label="git">Git 仓库</el-radio-button>
          <el-radio-button label="workspace">调试快照</el-radio-button>
        </el-radio-group>
      </div>

      <div v-if="form.codeSourceType === 'git'" class="space-y-4">
        <div>
          <label class="field-label">Git 仓库地址</label>
          <el-input v-model="form.gitURL" placeholder="https://gitlab.qomolo.com/dl/bevfusion.git" @input="clearResolved" />
        </div>
        <div>
          <label class="field-label">分支、标签或 Commit</label>
          <div class="flex gap-3">
            <el-input v-model="form.gitRef" class="flex-1" placeholder="例如：bev_3dod" @keyup.enter="resolve" @input="clearResolved" />
            <el-button :loading="resolving" @click="resolve">解析</el-button>
          </div>
          <p class="field-help">填分支名即可，平台会解析成固定 Commit 后再提交，保证同一个任务永远运行同一份代码。</p>
          <div v-if="form.gitCommit" class="mt-3 rounded-xl border border-emerald-800/60 bg-emerald-950/20 px-4 py-3">
            <p class="text-xs text-emerald-200">已固定 Commit</p>
            <p class="mt-1 break-all font-mono text-xs text-emerald-100">{{ form.gitCommit }}</p>
          </div>
          <p v-if="resolveError" class="mt-2 text-xs text-amber-300">{{ resolveError }}</p>
          <router-link to="/account-security" class="mt-2 inline-block text-xs text-blue-300 hover:text-blue-200">
            私有仓库？先在“账户与安全”添加个人 Git 凭据。
          </router-link>
        </div>
      </div>

      <div v-else>
        <label class="field-label">工作区代码版本</label>
        <el-select v-model="form.workspaceSnapshot" class="w-full" filterable clearable :loading="loading" placeholder="选择已创建的不可变代码版本">
          <el-option v-for="snapshot in snapshots" :key="snapshot.id" :label="snapshotLabel(snapshot)" :value="snapshot.id" />
        </el-select>
        <p class="field-help">
          版本由“我的数据 → 我的工作区”创建；训练会只读复制该版本到 <code>{{ workspacePath }}</code>，不会使用可变工作区或对象存储密钥。
        </p>
        <router-link to="/datacache" class="mt-2 inline-block text-xs text-blue-300 hover:text-blue-200">还没有版本？前往我的工作区创建。</router-link>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'

import { resolveGitRef } from '../../api/platform'

const props = defineProps({
  form: { type: Object, required: true },
  images: { type: Array, default: () => [] },
  snapshots: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false },
  workspacePath: { type: String, default: '/workspace' },
})

const resolving = ref(false)
const resolveError = ref('')

const clearResolved = () => {
  props.form.gitCommit = ''
  resolveError.value = ''
}

// The platform still stores a commit; this only removes the manual step of
// copying a SHA out of GitLab.
const resolve = async () => {
  const repositoryUrl = String(props.form.gitURL || '').trim()
  const ref_ = String(props.form.gitRef || '').trim()
  if (!repositoryUrl || !ref_) {
    resolveError.value = '请先填写仓库地址和分支名'
    return
  }
  resolving.value = true
  resolveError.value = ''
  try {
    const resolved = await resolveGitRef(repositoryUrl, ref_)
    props.form.gitCommit = resolved.commit
  } catch (error) {
    props.form.gitCommit = ''
    resolveError.value = error.message || '解析失败，请确认分支名与仓库权限'
  } finally {
    resolving.value = false
  }
}

const imageLabel = (image) => {
  const labels = [image.name, image.framework].filter(Boolean)
  return `${labels.join(' · ')}${image.isDefault ? '（默认）' : ''}`
}

const snapshotLabel = (snapshot) => {
  const location = snapshot.sourcePath ? `工作区/${snapshot.sourcePath}` : '工作区根目录'
  const createdAt = snapshot.createdAt ? new Date(snapshot.createdAt).toLocaleString('zh-CN', { hour12: false }) : ''
  return `${location} · ${snapshot.fileCount || 0} 个文件${createdAt ? ` · ${createdAt}` : ''}`
}
</script>
