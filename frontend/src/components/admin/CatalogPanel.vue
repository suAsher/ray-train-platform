<template>
  <div class="space-y-8">
    <section class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h4 class="flex items-center gap-2 text-sm font-bold text-white">
            <el-icon class="text-emerald-400"><Box /></el-icon> 镜像目录（训练 / 调试运行环境）
          </h4>
          <p class="mt-1 text-[11px] text-slate-500">
            用户提交任务和启动调试环境时只能从这里选择，保证依赖环境一致且可复现。必须使用 sha256 digest。
          </p>
        </div>
        <el-button size="small" icon="Plus" @click="$emit('create-image')">登记镜像</el-button>
      </div>

      <el-alert v-if="images.length === 0" type="warning" show-icon :closable="false">
        <template #title>镜像目录为空，用户无法提交训练任务</template>
        目录为空时平台会退回到部署级 allowlist。请至少登记一个训练镜像和一个调试镜像。
      </el-alert>

      <el-table :data="images" class="!bg-transparent text-xs" empty-text="尚未登记任何镜像">
        <el-table-column prop="name" label="名称" min-width="150" />
        <el-table-column prop="kind" label="用途" width="110">
          <template #default="scope">
            <el-tag size="small" :type="scope.row.kind === 'training' ? 'primary' : 'warning'" effect="plain">
              {{ scope.row.kind === 'training' ? '训练' : '调试' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="framework" label="框架" width="120" />
        <el-table-column prop="reference" label="镜像（digest）" min-width="260">
          <template #default="scope">
            <span class="break-all font-mono text-[11px] text-slate-400">{{ scope.row.reference }}</span>
          </template>
        </el-table-column>
        <el-table-column label="范围" width="100">
          <template #default="scope">
            <el-tag size="small" effect="plain">{{ scope.row.tenantId ? '本团队' : '全平台' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90" align="right">
          <template #default="scope">
            <el-button type="danger" link size="small" @click="$emit('remove-image', scope.row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <section class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h4 class="flex items-center gap-2 text-sm font-bold text-white">
            <el-icon class="text-blue-400"><Key /></el-icon> 团队私有 Git 凭证
          </h4>
          <p class="mt-1 text-[11px] text-slate-500">
            团队共用兜底凭据。个人凭据由用户在「账户与安全」自行管理；令牌只写入 Kubernetes Secret，不回显。
          </p>
        </div>
        <el-button size="small" icon="Plus" @click="$emit('create-credential')">添加团队凭据</el-button>
      </div>

      <el-table :data="credentials" class="!bg-transparent text-xs" empty-text="尚未配置私有仓库凭证">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column prop="host" label="Git 主机" min-width="180" />
        <el-table-column prop="username" label="用户名" width="140" />
        <el-table-column label="操作" width="150" align="right">
          <template #default="scope">
            <el-button type="primary" link size="small" @click="$emit('test-credential', scope.row)">测试</el-button>
            <el-button type="danger" link size="small" @click="$emit('remove-credential', scope.row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </div>
</template>

<script setup>
defineProps({
  images: { type: Array, default: () => [] },
  credentials: { type: Array, default: () => [] },
})

defineEmits(['create-image', 'remove-image', 'create-credential', 'remove-credential', 'test-credential'])
</script>
