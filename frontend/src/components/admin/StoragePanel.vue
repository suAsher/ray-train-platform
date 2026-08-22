<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h4 class="text-sm font-bold text-white">数据集与存储资源</h4>
        <p class="mt-1 text-xs leading-5 text-slate-400">
          把管理员已创建的 PVC 登记为用户可选的数据来源。用户在「新建训练任务 → 训练输入」中按名称选择，
          看不到也不需要知道 PVC 名或 TOS 前缀。
        </p>
      </div>
      <el-button size="small" icon="Plus" @click="dialogVisible = true">登记存储资源</el-button>
    </div>

    <el-table :data="assets" class="!bg-transparent text-xs" empty-text="尚未登记任何存储资源">
      <el-table-column prop="name" label="名称" min-width="180" />
      <el-table-column prop="kind" label="用途" width="110">
        <template #default="scope">
          <el-tag size="small" effect="plain" :type="kindTag(scope.row.kind)">{{ kindLabel(scope.row.kind) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="claimName" label="PVC" min-width="220">
        <template #default="scope"><code class="font-mono text-[11px] text-slate-400">{{ scope.row.claimName }}</code></template>
      </el-table-column>
      <el-table-column label="权限" width="100">
        <template #default="scope">
          <el-tag size="small" effect="plain" :type="scope.row.readOnly ? 'info' : 'warning'">
            {{ scope.row.readOnly ? '只读' : '可写' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="范围" width="100">
        <template #default="scope">
          <el-tag size="small" effect="plain">{{ scope.row.tenantId ? '本团队' : '全平台' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="90" align="right">
        <template #default="scope">
          <el-button type="danger" link size="small" @click="remove(scope.row.id)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="dialogVisible" title="登记存储资源" width="min(520px, 94vw)">
      <el-alert type="info" :closable="false" show-icon class="mb-4">
        <template #title>PVC 必须已经存在于租户 namespace 中</template>
        平台不会替你创建 PVC，也不会从用户输入推导 TOS 路径。请先由运维创建好静态 PV/PVC，再在这里登记。
      </el-alert>
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="显示名称">
          <el-input v-model="form.name" placeholder="例如：BEVFusion 公共数据集 0429" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" placeholder="可选，帮助用户判断该选哪个" />
        </el-form-item>
        <el-form-item label="用途">
          <el-select v-model="form.kind" class="w-full">
            <el-option label="训练输入（dataset）" value="dataset" />
            <el-option label="初始 Checkpoint（checkpoint）" value="checkpoint" />
            <el-option label="训练产物（output）" value="output" />
          </el-select>
        </el-form-item>
        <el-form-item label="PVC 名称">
          <el-input v-model="form.claimName" placeholder="例如：data-bevfusion-datasets" />
          <p class="field-help">必须是租户 namespace 里已 Bound 的 PVC。</p>
        </el-form-item>
        <div class="flex flex-wrap gap-6">
          <el-checkbox v-model="form.readOnly">只读挂载</el-checkbox>
          <el-checkbox v-model="form.browseEnabled">允许在页面浏览目录</el-checkbox>
          <el-checkbox v-model="form.shared" :disabled="!isSuperAdmin">全平台共享</el-checkbox>
        </div>
        <p class="mt-2 text-[11px] leading-5 text-slate-500">
          训练输入与 Checkpoint 必须只读；训练产物必须可写。平台会在提交时再次校验。
        </p>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submit">登记</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'

import { createStorageAsset, deleteStorageAsset, fetchStorageAssets } from '../../api/storageAssets'

defineProps({ isSuperAdmin: { type: Boolean, default: false } })

const assets = ref([])
const dialogVisible = ref(false)
const saving = ref(false)
const form = ref({ name: '', description: '', kind: 'dataset', claimName: '', readOnly: true, browseEnabled: true, shared: false })

const kindLabel = (kind) => ({ dataset: '训练输入', checkpoint: 'Checkpoint', output: '训练产物' }[kind] || kind)
const kindTag = (kind) => ({ dataset: 'primary', checkpoint: 'warning', output: 'success' }[kind] || 'info')

const load = async () => {
  try {
    assets.value = await fetchStorageAssets()
  } catch {
    assets.value = []
  }
}

const submit = async () => {
  if (!form.value.name.trim() || !form.value.claimName.trim()) {
    ElMessage.warning('请填写名称与 PVC 名称')
    return
  }
  // Input and checkpoint roots must stay read-only; the server rejects the
  // other combinations, so the mistake is caught here rather than at submit.
  if (form.value.kind !== 'output' && !form.value.readOnly) {
    ElMessage.warning('训练输入与 Checkpoint 必须为只读')
    return
  }
  if (form.value.kind === 'output' && form.value.readOnly) {
    ElMessage.warning('训练产物必须可写')
    return
  }
  saving.value = true
  try {
    await createStorageAsset({ ...form.value, provider: 'tos' })
    ElMessage.success(`已登记「${form.value.name}」，用户现在可以在训练输入中选择它`)
    dialogVisible.value = false
    form.value = { name: '', description: '', kind: 'dataset', claimName: '', readOnly: true, browseEnabled: true, shared: false }
    await load()
  } catch (error) {
    ElMessage.error(error.message || '登记存储资源失败')
  } finally {
    saving.value = false
  }
}

const remove = async (id) => {
  try {
    await deleteStorageAsset(id)
    ElMessage.success('已删除')
    await load()
  } catch (error) {
    ElMessage.error(error.message || '删除失败')
  }
}

onMounted(load)
</script>
