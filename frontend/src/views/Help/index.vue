<template>
  <div class="space-y-6">
    <section class="panel p-6">
      <div class="flex flex-wrap items-center justify-between gap-4">
        <div><h3 class="text-2xl font-bold text-white">平台使用说明</h3><p class="mt-2 max-w-3xl text-sm text-slate-400">按场景、参数或错误关键词搜索；这里和下载文件都只展示已发布内容。</p></div>
        <div class="flex gap-2"><el-button v-if="isSuperAdmin" @click="manage = true">管理文档</el-button><el-button type="primary" :disabled="!documents.length || loading || !!error" @click="download">下载为 Markdown</el-button></div>
      </div>
    </section>
    <el-alert v-if="error" type="error" :title="error" :closable="false"><el-button @click="load">重新加载</el-button></el-alert>
    <p v-if="loading" role="status">正在加载文档…</p>
    <el-alert v-if="!loading && !error && missingLink" type="warning" title="此链接对应的文档不存在或已下架。你仍可从目录查看其他已发布文档。" :closable="false" />
    <div v-if="!loading && !error" class="grid gap-6 xl:grid-cols-[16rem_minmax(0,1fr)]">
      <nav class="panel h-fit p-3" aria-label="使用说明目录">
        <label for="help-search" class="mb-2 block text-xs text-slate-400">搜索主题、参数或错误</label>
        <el-input id="help-search" v-model="query" clearable placeholder="例如：413、场地、续训" />
        <p class="my-3 text-xs text-slate-500" role="status">{{ filteredSections.length }} / {{ documents.length }} 个主题</p>
        <p v-if="!filteredSections.length" class="text-sm text-slate-400">{{ documents.length ? '没有匹配主题，试试更短的关键词。' : '暂无已发布文档。' }}</p>
        <div v-for="group in groupedSections" :key="group.name" class="mb-4">
          <p class="my-2 text-xs text-slate-500">{{ group.name }}</p>
          <button v-for="section in group.sections" :key="section.id" class="block w-full rounded px-3 py-2 text-left text-sm" :class="section.id === activeSection?.id ? 'bg-blue-500/15 text-blue-200' : 'text-slate-400'" :aria-current="section.id === activeSection?.id ? 'page' : undefined" @click="select(section.id)">{{ section.title }}</button>
        </div>
      </nav>
      <section v-if="activeSection" class="panel min-w-0 p-6" aria-labelledby="help-topic-title">
        <h4 id="help-topic-title" ref="topicTitle" tabindex="-1" class="text-xl font-bold text-white">{{ activeSection.title }}</h4>
        <p class="my-2 text-xs text-slate-500">版本 {{ activeSection.version }} · {{ formatTime(activeSection.updatedAt) }} · {{ activeSection.updatedBy }}</p>
        <HelpMarkdown :markdown="activeSection.markdown" />
      </section>
    </div>
    <HelpManager v-if="manage && isSuperAdmin" @close="manage = false" @published="load" />
  </div>
</template>
<script setup>
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { roles } from '../../stores/session'
import { listHelpDocuments } from '../../api/help.js'
import { searchDocuments, downloadDocuments, missingDocumentLink } from '../../help/documents.js'
import { createDocumentLoader } from '../../help/loader.js'
import { saveBlobAsFile } from '../../checkpointDownload'
import HelpMarkdown from '../../components/HelpMarkdown.vue'
import HelpManager from './HelpManager.vue'
const route = useRoute(), router = useRouter()
const { documents, loading, error, load } = createDocumentLoader(listHelpDocuments)
const query = ref(''), manage = ref(false), topicTitle = ref(null)
const isSuperAdmin = computed(() => roles.value.includes('SuperAdmin'))
const filteredSections = computed(() => searchDocuments(documents.value, query.value))
const missingLink = computed(() => missingDocumentLink(documents.value, route.hash))
const activeSection = computed(() => filteredSections.value.find(section => '#' + section.id === route.hash) || filteredSections.value[0])
const groupedSections = computed(() => {
  const groups = new Map()
  for (const section of filteredSections.value) groups.set(section.category, [...(groups.get(section.category) || []), section])
  return [...groups].map(([name, sections]) => ({ name, sections }))
})
function formatTime(value) { return value ? new Date(value).toLocaleString() : '' }
function select(id) { if (route.hash !== '#' + id) router.push({ hash: '#' + id }) }
watch(() => route.hash, async () => { await nextTick(); topicTitle.value?.focus({ preventScroll: true }) })
function download() {
  try {
    const markdown = downloadDocuments(documents.value, { origin: window.location.origin })
    saveBlobAsFile(new Blob([markdown], { type: 'text/markdown;charset=utf-8' }), 'raytrain-使用说明.md')
  }
  catch (err) { ElMessage.error(err.message || '下载失败') }
}
onMounted(load)
</script>
