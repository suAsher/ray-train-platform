<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h4 class="text-sm font-bold text-white">平台账号与角色</h4>
        <p class="mt-1 text-xs text-slate-400">本地账号可在这里直接管理；企业 SSO 账号由 Keycloak 维护，这里只读。</p>
      </div>
      <div class="flex gap-2">
        <el-button v-if="isSuperAdmin && storageQuotaEnabled" plain size="small" :loading="preparing" @click="$emit('prepare-object-set')">初始化 TOS 目录配额</el-button>
        <el-button size="small" icon="User" @click="$emit('create-user')">添加用户</el-button>
      </div>
    </div>

    <el-input v-model="keyword" placeholder="按用户名或租户筛选" clearable prefix-icon="Search" class="!w-80" />

    <el-table :data="visibleUsers" class="!bg-transparent text-xs" empty-text="没有匹配的账号">
      <el-table-column prop="username" label="用户名" min-width="160" />
      <el-table-column prop="email" label="邮箱" min-width="200" />
      <el-table-column prop="role" label="角色" width="200">
        <template #default="scope">
          <div class="flex items-center gap-2">
            <el-tag :type="roleTag(scope.row.role)" size="small" effect="plain">{{ roleLabel(scope.row.role) }}</el-tag>
            <el-button v-if="isSuperAdmin && canManage(scope.row)" link size="small" @click="$emit('edit-roles', scope.row)">修改</el-button>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="tenant_id" label="所属租户" width="160" />
      <el-table-column v-if="storageQuotaEnabled" label="个人 TOS 容量" width="190">
        <template #default="scope">
          <div class="flex items-center gap-2">
            <el-tag v-if="scope.row.storageQuota?.enforced" type="success" size="small" effect="plain">
              {{ formatStorageQuota(scope.row.storageQuota.bytes) }}
            </el-tag>
            <span v-else class="text-[11px] text-slate-500">未启用</span>
            <el-button v-if="canManage(scope.row)" type="primary" link size="small" @click="$emit('edit-storage', scope.row)">配置</el-button>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="disabled" label="状态" width="100">
        <template #default="scope">
          <el-tag size="small" :type="scope.row.disabled ? 'info' : 'success'" effect="plain">
            {{ scope.row.disabled ? '已停用' : '正常' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="账户操作" width="250" align="right">
        <template #default="scope">
          <template v-if="canManage(scope.row)">
            <el-button type="primary" link size="small" @click="$emit('reset-password', scope.row)">重置密码</el-button>
            <el-button v-if="scope.row.disabled" type="success" link size="small" @click="$emit('set-state', scope.row, false)">启用</el-button>
            <el-button v-else type="danger" link size="small" @click="$emit('set-state', scope.row, true)">停用</el-button>
            <el-button type="danger" link size="small" @click="$emit('decommission', scope.row)">删除</el-button>
          </template>
          <span v-else class="text-[11px] text-slate-600">受保护</span>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue'

import { formatStorageQuota } from '../../storageQuota'

const props = defineProps({
  users: { type: Array, default: () => [] },
  isSuperAdmin: { type: Boolean, default: false },
  storageQuotaEnabled: { type: Boolean, default: false },
  preparing: { type: Boolean, default: false },
  canManage: { type: Function, required: true },
})

defineEmits(['create-user', 'reset-password', 'set-state', 'decommission', 'edit-storage', 'prepare-object-set', 'edit-roles'])

const keyword = ref('')

const visibleUsers = computed(() => {
  const needle = keyword.value.trim().toLowerCase()
  if (!needle) return props.users
  return props.users.filter((user) =>
    String(user.username || '').toLowerCase().includes(needle) ||
    String(user.tenant_id || '').toLowerCase().includes(needle))
})

// The bare role name does not tell an operator what it grants; the shared team
// directory is writable only by a tenant administrator.
const roleLabel = (role) => ({
  SuperAdmin: 'SuperAdmin · 全平台',
  TenantAdmin: 'TenantAdmin · 可写团队目录',
  Engineer: 'Engineer · 仅个人空间',
}[role] || role)

const roleTag = (role) => {
  switch (role) {
    case 'SuperAdmin': return 'danger'
    case 'TenantAdmin': return 'warning'
    default: return 'info'
  }
}
</script>
