<template>
  <el-dialog :model-value="true" title="管理使用说明 · 仅超级管理员" width="94%" top="3vh" :before-close="close" :close-on-click-modal="false">
    <p class="mb-4 text-sm text-slate-400">保存草稿不会改变用户看到的内容；发布后全平台可见。恢复历史版本只生成新草稿。ID 决定文档链接，创建后不可更改。</p>
    <el-alert v-if="error" class="mb-4" type="error" :title="error" :closable="false" />
    <div class="grid gap-5 lg:grid-cols-[15rem_minmax(0,1fr)]">
      <aside>
        <div class="mb-3 flex gap-2"><el-button :disabled="busy" @click="choose(null)">新增文档</el-button><el-button :disabled="busy" @click="reload">刷新列表</el-button></div>
        <p v-if="loading" role="status">正在加载…</p>
        <div class="max-h-[65vh] space-y-1 overflow-y-auto">
          <button v-for="doc in documents" :key="doc.id" :disabled="busy" class="block w-full rounded p-2 text-left" :class="doc.id === form.id ? 'bg-blue-500/20' : ''" @click="choose(doc)">
            <span class="block">{{ doc.title }}</span><small class="text-slate-500">{{ doc.publishedVersion ? '已发布 v' + doc.publishedVersion : '未发布' }} · 当前 v{{ doc.version }}</small>
          </button>
        </div>
      </aside>
      <div class="min-w-0">
        <el-form label-position="top" :disabled="busy">
          <div class="grid gap-3 md:grid-cols-3">
            <el-form-item label="文档 ID"><el-input v-model="form.id" :disabled="!!form.version" maxlength="80" placeholder="例如 training-faq" /></el-form-item>
            <el-form-item label="分类"><el-input v-model="form.category" maxlength="100" /></el-form-item>
            <el-form-item label="排序（数字越小越靠前）"><el-input-number v-model="form.sortOrder" :min="-100000" :max="100000" :precision="0" /></el-form-item>
          </div>
          <el-form-item label="标题"><el-input v-model="form.title" maxlength="200" /></el-form-item>
          <el-tabs v-model="tab">
            <el-tab-pane label="Markdown 编辑" name="edit"><el-input v-model="form.markdown" type="textarea" :rows="18" placeholder="使用 Markdown 编写步骤、表格和代码示例" /></el-tab-pane>
            <el-tab-pane label="预览" name="preview"><div class="max-h-[55vh] overflow-auto"><HelpMarkdown :markdown="form.markdown" /></div></el-tab-pane>
          </el-tabs>
        </el-form>
        <p class="my-2 text-xs text-slate-500">{{ dirty ? '有未保存修改' : '没有未保存修改' }} · 正文 {{ markdownKiB }} / 512 KiB。禁用原始 HTML 和图片；代码块支持复制，附加 filename=train.py 可提供模板下载。</p>
        <p v-if="form.version" class="my-2 text-xs text-slate-500">当前 v{{ form.version }} · 发布 {{ form.publishedVersion ? 'v' + form.publishedVersion : '无' }} · {{ formatTime(form.updatedAt) }} · {{ form.updatedBy }}</p>
        <div class="my-4 flex flex-wrap gap-2">
          <el-button type="primary" :loading="busy" @click="run(editor.save, '草稿已保存')">保存草稿</el-button>
          <el-button type="success" :disabled="busy || dirty || !form.version" @click="publish">发布</el-button>
          <el-button type="warning" :disabled="busy || dirty || !form.publishedVersion" @click="unpublish">下架</el-button>
          <el-button :disabled="busy || !form.version" @click="history">版本历史</el-button>
          <el-button :disabled="busy" @click="exportDraft">下载当前草稿备份</el-button>
        </div>
        <section v-if="revisions.length" class="border-t border-slate-700 pt-4">
          <h4>版本历史（恢复后需重新发布才对用户生效）</h4>
          <div class="my-3 max-h-48 overflow-auto">
            <div v-for="revision in revisions" :key="revision.version" class="flex flex-wrap items-center gap-3 border-b border-slate-800 py-2">
              <span>v{{ revision.version }} · {{ actionLabel(revision.action) }} · {{ formatTime(revision.updatedAt) }} · {{ revision.updatedBy }}</span>
              <el-button size="small" @click="historical = revision">查看</el-button>
              <el-button size="small" :disabled="busy || dirty" @click="restore(revision.version)">恢复为草稿</el-button>
            </div>
          </div>
          <div v-if="historical" class="max-h-80 overflow-auto rounded border border-slate-700 p-4"><h4>{{ historical.title }} · v{{ historical.version }}</h4><HelpMarkdown :markdown="historical.markdown" /></div>
        </section>
      </div>
    </div>
    <template #footer><el-button :disabled="busy" @click="close">关闭</el-button></template>
  </el-dialog>
</template>
<script setup>
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as api from '../../api/help.js'
import { createDocumentEditor } from '../../help/editor.js'
import { saveBlobAsFile } from '../../checkpointDownload.js'
import HelpMarkdown from '../../components/HelpMarkdown.vue'
const emit = defineEmits(['close', 'published'])
const editor = createDocumentEditor(api)
const { form, dirty } = editor
const markdownKiB = computed(() => Math.ceil(new TextEncoder().encode(form.value.markdown).length / 1024))
const documents = ref([]), loading = ref(false), busy = ref(false), error = ref(''), tab = ref('edit'), revisions = ref([]), historical = ref(null)
const formatTime = value => value ? new Date(value).toLocaleString() : ''
const actionLabel = action => ({ create: '创建', seed: '初始化', save: '保存', publish: '发布', unpublish: '下架', restore: '恢复' }[action] || action)
async function confirm(message) { try { await ElMessageBox.confirm(message, '确认', { type: 'warning', confirmButtonText: '确定', cancelButtonText: '取消' }); return true } catch { return false } }
async function canLeave() { return !busy.value && (!dirty.value || await confirm('存在未保存的修改，确定放弃吗？可先下载草稿备份。')) }
async function close() { if (await canLeave()) emit('close') }
async function choose(doc) { if (await canLeave()) { editor.select(doc); revisions.value = []; historical.value = null; error.value = ''; tab.value = 'edit' } }
async function reload() {
  loading.value = true
  try { documents.value = (await api.listHelpDrafts()).items || [] }
  catch (err) { error.value = err.message || '文档列表加载失败' }
  finally { loading.value = false }
}
async function run(operation, message, published = false) {
  if (busy.value) return
  busy.value = true; error.value = ''
  try { await operation(); revisions.value = []; historical.value = null; ElMessage.success(message); if (published) emit('published'); await reload() }
  catch (err) { error.value = err.message || '操作失败；未保存内容已保留' }
  finally { busy.value = false }
}
async function publish() { if (await confirm('将当前已保存草稿发布给全平台用户？')) await run(editor.publish, '文档已发布', true) }
async function unpublish() { if (await confirm('下架后普通用户无法查看该文档，已有链接也不再显示此文档。历史仍保留。')) await run(editor.unpublish, '文档已下架', true) }
async function restore(version) { if (await confirm('将版本 v' + version + ' 恢复为新草稿？当前已发布内容不变。')) await run(() => editor.restore(version), '历史版本已恢复为草稿') }
async function history() {
  busy.value = true; error.value = ''
  try { revisions.value = (await api.helpDocumentHistory(form.value.id)).items || []; historical.value = null }
  catch (err) { error.value = err.message || '历史加载失败' }
  finally { busy.value = false }
}
function exportDraft() {
  try { saveBlobAsFile(new Blob([form.value.markdown], { type: 'text/markdown;charset=utf-8' }), 'help-draft.md') }
  catch (err) { ElMessage.error(err.message || '草稿备份失败') }
}
function beforeUnload(event) { if (dirty.value || busy.value) { event.preventDefault(); event.returnValue = '' } }
onBeforeRouteLeave(canLeave)
onMounted(() => { reload(); window.addEventListener('beforeunload', beforeUnload) })
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))
</script>
