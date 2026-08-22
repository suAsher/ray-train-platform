<template>
  <div class="max-w-3xl space-y-6">
    <section class="bg-[#131826] border border-slate-800/80 rounded-2xl p-7 shadow-xl">
      <p class="text-[11px] uppercase tracking-[0.2em] text-blue-400 font-semibold">Account & Security</p>
      <h3 class="mt-2 text-xl font-bold text-white">账户与安全</h3>
      <p class="mt-2 text-sm text-slate-400">管理当前登录账号的认证方式与密码。修改本地密码后，所有已登录设备都会退出。</p>
    </section>

    <section class="bg-[#131826] border border-slate-800/80 rounded-2xl p-6 shadow-xl space-y-4">
      <div class="flex items-center justify-between">
        <h4 class="font-bold text-white">当前身份</h4>
        <el-tag :type="isLocal ? 'warning' : 'success'" effect="plain">{{ isLocal ? '平台本地账号' : '企业 SSO / Keycloak' }}</el-tag>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 text-sm">
        <div class="rounded-xl bg-slate-950/60 border border-slate-800 px-4 py-3"><p class="text-xs text-slate-500">用户名</p><p class="mt-1 text-slate-200 font-mono">{{ user?.username || '—' }}</p></div>
        <div class="rounded-xl bg-slate-950/60 border border-slate-800 px-4 py-3"><p class="text-xs text-slate-500">所属团队</p><p class="mt-1 text-slate-200 font-mono">{{ user?.tenantId || '—' }}</p></div>
      </div>
    </section>

    <section v-if="isLocal" class="bg-[#131826] border border-amber-500/25 rounded-2xl p-6 shadow-xl">
      <div class="flex items-start gap-3 mb-5">
        <el-icon class="text-amber-400 mt-0.5"><Lock /></el-icon>
        <div><h4 class="font-bold text-white">修改本地密码</h4><p class="mt-1 text-xs text-slate-400">至少 8 位。成功后需要用新密码重新登录。</p></div>
      </div>
      <el-alert v-if="errorMessage" :title="errorMessage" type="error" show-icon :closable="false" class="mb-4 !rounded-xl" />
      <el-form label-position="top" @submit.prevent="submit">
        <el-form-item label="当前密码"><el-input v-model="currentPassword" type="password" show-password autocomplete="current-password" :disabled="saving" /></el-form-item>
        <el-form-item label="新密码"><el-input v-model="newPassword" type="password" show-password autocomplete="new-password" :disabled="saving" /></el-form-item>
        <el-form-item label="确认新密码"><el-input v-model="confirmPassword" type="password" show-password autocomplete="new-password" :disabled="saving" @keyup.enter="submit" /></el-form-item>
        <div class="flex justify-end"><el-button type="primary" :loading="saving" @click="submit">保存并重新登录</el-button></div>
      </el-form>
    </section>

    <section class="bg-[#131826] border border-slate-800/80 rounded-2xl p-6 shadow-xl space-y-5">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h4 class="font-bold text-white">我的私有 Git 仓库凭据</h4>
          <p class="mt-1 text-xs leading-5 text-slate-400">用于拉取你自己的私有训练代码。令牌只写入本团队的 Kubernetes Secret，不会在页面或数据库中回显；你自己的凭据优先于团队公共凭据。</p>
        </div>
        <el-button size="small" type="primary" @click="showCredentialDialog = true">添加个人凭据</el-button>
      </div>

      <el-alert v-if="credentialError" :title="credentialError" type="warning" show-icon :closable="false" />
      <el-table :data="personalCredentials" class="!bg-transparent text-xs" empty-text="尚未配置个人私有仓库凭据">
        <el-table-column prop="name" label="名称" min-width="150" />
        <el-table-column prop="host" label="Git 主机" min-width="210" />
        <el-table-column prop="username" label="用户名" min-width="150" />
        <el-table-column label="用途" width="110"><template #default><el-tag size="small" effect="plain">仅本人</el-tag></template></el-table-column>
        <el-table-column label="操作" width="150" align="right"><template #default="scope"><el-button type="primary" link size="small" @click="testCredential(scope.row)">测试</el-button><el-button type="danger" link size="small" @click="removeCredential(scope.row.id)">删除</el-button></template></el-table-column>
      </el-table>
    </section>

    <section class="bg-[#131826] border border-slate-800/80 rounded-2xl p-6 shadow-xl space-y-5">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h4 class="font-bold text-white">个人访问令牌</h4>
          <p class="mt-1 text-xs leading-5 text-slate-400">用于集群外的 spk-rayjob。令牌仅在创建后显示一次；撤销后该设备立即不能提交、查看或取消任务。</p>
        </div>
        <el-button size="small" type="primary" @click="showTokenDialog = true">创建访问令牌</el-button>
      </div>
      <el-table :data="personalAccessTokens" class="!bg-transparent text-xs" empty-text="尚未创建访问令牌">
        <el-table-column prop="publicId" label="令牌标识" min-width="180" />
        <el-table-column label="权限" min-width="200"><template #default="scope">{{ (scope.row.scopes || []).join(' · ') }}</template></el-table-column>
        <el-table-column label="到期时间" min-width="180"><template #default="scope">{{ formatDate(scope.row.expiresAt) }}</template></el-table-column>
        <el-table-column label="最后使用" min-width="170"><template #default="scope">{{ scope.row.lastUsedAt ? formatDate(scope.row.lastUsedAt) : '尚未使用' }}</template></el-table-column>
        <el-table-column label="操作" width="90" align="right"><template #default="scope"><el-button type="danger" link size="small" :disabled="Boolean(scope.row.revokedAt)" @click="revokeToken(scope.row)">{{ scope.row.revokedAt ? '已撤销' : '撤销' }}</el-button></template></el-table-column>
      </el-table>
    </section>

    <el-alert v-if="!isLocal" title="该账号由企业身份系统管理，请在 Keycloak 或企业目录中修改密码。" type="info" show-icon :closable="false" class="!rounded-xl" />

    <el-dialog v-model="showCredentialDialog" title="添加个人 Git 凭据" width="min(460px, 94vw)">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="名称"><el-input v-model="newCredential.name" placeholder="例如：我的 GitLab" /></el-form-item>
        <el-form-item label="Git 主机"><el-input v-model="newCredential.host" placeholder="git.example.com（只填主机名）" /></el-form-item>
        <el-form-item label="用户名"><el-input v-model="newCredential.username" placeholder="留空时使用 git" /></el-form-item>
        <el-form-item label="访问令牌 / 密码"><el-input v-model="newCredential.token" type="password" show-password autocomplete="new-password" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="showCredentialDialog = false">取消</el-button><el-button type="primary" :loading="savingCredential" @click="saveCredential">保存</el-button></template>
    </el-dialog>

    <el-dialog v-model="showTokenDialog" title="创建个人访问令牌" width="min(460px, 94vw)">
      <p class="text-sm leading-6 text-slate-500">用于 spk-rayjob 的最小权限：提交、查看任务和上传代码。请设置过期时间，令牌可随时撤销。</p>
      <el-form class="mt-4" label-position="top"><el-form-item label="有效期（天，1–365）"><el-input-number v-model="tokenDays" :min="1" :max="365" /></el-form-item></el-form>
      <template #footer><el-button @click="showTokenDialog = false">取消</el-button><el-button type="primary" :loading="savingToken" @click="createToken">创建并显示一次</el-button></template>
    </el-dialog>

    <el-dialog v-model="showIssuedToken" title="保存访问令牌" width="min(540px, 94vw)" @closed="issuedToken = ''">
      <el-alert title="这是唯一一次显示令牌。请立即复制到外部调试机，关闭此窗口后将无法再次查看。" type="warning" show-icon :closable="false" />
      <el-input class="mt-4" :model-value="issuedToken" readonly type="textarea" :rows="3" />
      <template #footer><el-button @click="copyIssuedToken">复制令牌</el-button><el-button type="primary" @click="showIssuedToken = false">我已保存</el-button></template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Lock } from '@element-plus/icons-vue'
import { changeLocalPassword } from '../../auth/localSession'
import { createGitCredential, createPersonalAccessToken, deleteGitCredential, fetchGitCredentials, fetchPersonalAccessTokens, revokePersonalAccessToken, testGitCredential } from '../../api/catalog'
import { session } from '../../stores/session'

const router = useRouter()
const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const saving = ref(false)
const errorMessage = ref('')
const personalCredentials = ref([])
const credentialError = ref('')
const showCredentialDialog = ref(false)
const savingCredential = ref(false)
const newCredential = ref({ name: '', host: '', username: '', token: '', scope: 'personal' })
const personalAccessTokens = ref([])
const showTokenDialog = ref(false)
const showIssuedToken = ref(false)
const savingToken = ref(false)
const tokenDays = ref(90)
const issuedToken = ref('')
const user = computed(() => session.value)
const isLocal = computed(() => user.value?.authType === 'local')

async function loadCredentials() {
  credentialError.value = ''
  try {
    personalCredentials.value = (await fetchGitCredentials()).filter((credential) => credential.scope === 'personal')
  } catch (error) {
    personalCredentials.value = []
    credentialError.value = error.message || '无法读取个人 Git 凭据'
  }
}

async function loadPersonalAccessTokens() {
  try {
    personalAccessTokens.value = await fetchPersonalAccessTokens()
  } catch {
    personalAccessTokens.value = []
  }
}

async function createToken() {
  savingToken.value = true
  try {
    const issued = await createPersonalAccessToken({ scopes: ['jobs:read', 'jobs:write', 'sources:write'], expiresInDays: tokenDays.value })
    issuedToken.value = issued.token || ''
    if (!issuedToken.value) throw new Error('平台未返回访问令牌')
    showTokenDialog.value = false
    showIssuedToken.value = true
    await loadPersonalAccessTokens()
  } catch (error) {
    ElMessage.error(error.message || '创建访问令牌失败')
  } finally {
    savingToken.value = false
  }
}

async function revokeToken(token) {
  try {
    await ElMessageBox.confirm(`撤销 ${token.publicId} 后，使用它的所有 spk-rayjob 客户端都会立刻失效。`, '撤销访问令牌', { type: 'warning', confirmButtonText: '撤销', cancelButtonText: '取消' })
    await revokePersonalAccessToken(token.id)
    ElMessage.success('访问令牌已撤销')
    await loadPersonalAccessTokens()
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error.message || '撤销访问令牌失败')
  }
}

async function copyIssuedToken() {
  try {
    await navigator.clipboard.writeText(issuedToken.value)
    ElMessage.success('访问令牌已复制')
  } catch {
    ElMessage.warning('浏览器未允许自动复制，请手动复制令牌')
  }
}

function formatDate(value) {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—'
}

async function saveCredential() {
  if (!newCredential.value.host || !newCredential.value.token) {
    ElMessage.warning('请填写 Git 主机与访问令牌')
    return
  }
  savingCredential.value = true
  try {
    await createGitCredential({ ...newCredential.value, scope: 'personal' })
    showCredentialDialog.value = false
    newCredential.value = { name: '', host: '', username: '', token: '', scope: 'personal' }
    ElMessage.success('个人 Git 凭据已保存')
    await loadCredentials()
  } catch (error) {
    ElMessage.error(error.message || '保存个人 Git 凭据失败')
  } finally {
    savingCredential.value = false
  }
}

async function removeCredential(id) {
  try {
    await deleteGitCredential(id)
    ElMessage.success('个人 Git 凭据已删除')
    await loadCredentials()
  } catch (error) {
    ElMessage.error(error.message || '删除个人 Git 凭据失败')
  }
}

async function testCredential(credential) {
  try {
    const { value } = await ElMessageBox.prompt(
      `输入 ${credential.host} 上一个你有只读权限的 HTTPS 仓库地址。平台只会访问此已批准的主机，不会显示令牌。`,
      '测试 Git 凭据',
      { inputPlaceholder: `https://${credential.host}/group/repository.git`, inputPattern: /^https:\/\//, inputErrorMessage: '请输入 HTTPS 仓库地址', confirmButtonText: '开始测试', cancelButtonText: '取消' },
    )
    const result = await testGitCredential(credential.id, value)
    if (result.authenticated) ElMessage.success(result.message || '仓库连接与权限验证成功')
    else ElMessage.warning(result.message || 'Git 主机可达，但凭据没有该仓库权限')
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error.message || 'Git 凭据测试失败')
  }
}

async function submit() {
  if (saving.value) return
  errorMessage.value = ''
  if (!currentPassword.value || !newPassword.value || !confirmPassword.value) {
    errorMessage.value = '请填写当前密码、新密码和确认密码'
    return
  }
  if (newPassword.value.length < 8) {
    errorMessage.value = '新密码至少需要 8 位'
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    errorMessage.value = '两次输入的新密码不一致'
    return
  }
  saving.value = true
  try {
    await changeLocalPassword(currentPassword.value, newPassword.value)
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
    ElMessage.success('密码已更新，请使用新密码重新登录')
    window.setTimeout(() => router.replace('/login'), 350)
  } catch (error) {
    errorMessage.value = error.message || '修改密码失败'
  } finally {
    saving.value = false
  }
}

onMounted(async () => {
  await Promise.all([loadCredentials(), loadPersonalAccessTokens()])
})
</script>
