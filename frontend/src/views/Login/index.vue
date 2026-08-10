<template>
  <div class="min-h-screen flex items-center justify-center bg-[#0A0D14] px-4">
    <div class="w-full max-w-md">
      <!-- Brand -->
      <div class="flex items-center gap-3 mb-8 justify-center">
        <div class="w-12 h-12 rounded-2xl bg-gradient-to-br from-blue-500 to-indigo-600 flex items-center justify-center shadow-lg shadow-blue-900/40">
          <el-icon class="text-white" :size="24"><Cpu /></el-icon>
        </div>
        <div>
          <h1 class="text-xl font-bold text-white tracking-wide">Ray AI Platform</h1>
          <p class="text-xs text-slate-400">多租户分布式训练控制台</p>
        </div>
      </div>

      <div class="bg-[#131826] border border-slate-800/80 rounded-2xl shadow-2xl p-8 space-y-6">
        <div class="space-y-1">
          <h2 class="text-lg font-bold text-white">登录</h2>
          <p class="text-xs text-slate-400">使用平台本地账号登录，或通过企业 SSO 登录。</p>
        </div>

        <el-alert v-if="errorMessage" :title="errorMessage" type="error" show-icon :closable="false" class="!rounded-xl" />

        <form v-if="providers.local" class="space-y-4" @submit.prevent="submit">
          <div class="space-y-2">
            <label class="text-xs font-semibold text-slate-300">用户名</label>
            <el-input v-model="username" size="large" placeholder="admin" autocomplete="username" :disabled="loading" @keyup.enter="submit">
              <template #prefix><el-icon><User /></el-icon></template>
            </el-input>
          </div>

          <div class="space-y-2">
            <label class="text-xs font-semibold text-slate-300">密码</label>
            <el-input v-model="password" type="password" size="large" placeholder="••••••••" autocomplete="current-password" show-password :disabled="loading" @keyup.enter="submit">
              <template #prefix><el-icon><Lock /></el-icon></template>
            </el-input>
          </div>

          <el-button type="primary" size="large" class="w-full !rounded-xl" :loading="loading" native-type="submit">
            登录
          </el-button>
        </form>

        <div v-if="providers.local && providers.oidc" class="flex items-center gap-3">
          <div class="h-px bg-slate-800 flex-1"></div>
          <span class="text-[11px] text-slate-500">或</span>
          <div class="h-px bg-slate-800 flex-1"></div>
        </div>

        <el-button v-if="providers.oidc" size="large" class="w-full !rounded-xl" :disabled="loading" @click="ssoLogin">
          使用企业 SSO 登录 (Keycloak)
        </el-button>

        <el-alert
          v-if="!providers.local && !providers.oidc"
          title="没有可用的登录方式，请检查后端 LOCAL_AUTH_ENABLED 或 Keycloak 配置。"
          type="warning"
          show-icon
          :closable="false"
          class="!rounded-xl"
        />
      </div>

      <p class="text-center text-[11px] text-slate-600 mt-6">
        Ray 分布式训练平台 · KubeRay + Kueue
      </p>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Cpu, User, Lock } from '@element-plus/icons-vue'
import { loginWithPassword, fetchAuthProviders } from '../../auth'
import { login as ssoRedirect } from '../../auth/keycloak'

const router = useRouter()
const route = useRoute()

const username = ref('')
const password = ref('')
const loading = ref(false)
const errorMessage = ref('')
const providers = ref({ local: true, oidc: false })

onMounted(async () => {
  providers.value = await fetchAuthProviders()
})

async function submit() {
  if (loading.value) return
  errorMessage.value = ''
  if (!username.value || !password.value) {
    errorMessage.value = '请输入用户名和密码'
    return
  }
  loading.value = true
  try {
    await loginWithPassword(username.value, password.value)
    // A full reload lets every module pick up the new session cleanly.
    window.location.assign(route.query.redirect || '/job')
  } catch (error) {
    errorMessage.value = error.message || '登录失败'
  } finally {
    loading.value = false
  }
}

function ssoLogin() {
  ssoRedirect()
}
</script>
