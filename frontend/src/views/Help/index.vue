<template>
  <section class="space-y-6">
    <div class="rounded-2xl border border-slate-800 bg-gradient-to-br from-slate-900 to-slate-950 p-6">
      <p class="text-[11px] font-semibold uppercase tracking-[0.2em] text-blue-400">Usage</p>
      <div class="mt-2 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h3 class="text-2xl font-bold text-white">平台使用说明</h3>
          <p class="mt-3 max-w-3xl text-sm leading-6 text-slate-300">
            这里只放决定任务成败的部分：代码怎么和平台对接、启动命令怎么写、数据模式怎么选、报错了怎么办。
            每条都对应真实发生过的失败。可以直接下载成 Markdown 带走。
          </p>
        </div>
        <el-button type="primary" :loading="downloading" @click="download">下载为 Markdown</el-button>
      </div>
      <nav class="mt-5 flex flex-wrap gap-2">
        <el-button v-for="section in helpSections" :key="section.id" size="small" @click="scrollTo(section.id)">
          {{ section.title }}
        </el-button>
      </nav>
    </div>

    <article v-for="section in helpSections" :id="`help-${section.id}`" :key="section.id" class="rounded-2xl border border-slate-800 bg-slate-950/50 p-6">
      <h4 class="text-lg font-bold text-white">{{ section.title }}</h4>
      <p v-if="section.summary" class="mt-2 max-w-3xl text-sm leading-6 text-slate-400">{{ section.summary }}</p>

      <template v-for="(block, index) in section.blocks" :key="index">
        <ol v-if="block.kind === 'steps'" class="mt-4 space-y-3">
          <li v-for="(item, step) in block.items" :key="item.title" class="flex gap-3">
            <span class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-blue-500/15 font-mono text-xs text-blue-300">{{ step + 1 }}</span>
            <div>
              <p class="text-sm font-semibold text-slate-100">{{ item.title }}</p>
              <p class="mt-1 text-sm leading-6 text-slate-400">{{ item.body }}</p>
            </div>
          </li>
        </ol>

        <ul v-else-if="block.kind === 'list'" class="mt-4 space-y-2">
          <li v-for="item in block.items" :key="item" class="flex gap-2 text-sm leading-6 text-slate-300">
            <span class="text-blue-400">·</span><span>{{ item }}</span>
          </li>
        </ul>

        <ul v-else-if="block.kind === 'checklist'" class="mt-4 space-y-2">
          <li v-for="item in block.items" :key="item" class="flex gap-2 text-sm leading-6 text-slate-300">
            <span class="mt-1.5 h-3 w-3 shrink-0 rounded border border-slate-600"></span><span>{{ item }}</span>
          </li>
        </ul>

        <div v-else-if="block.kind === 'table'" class="mt-4 overflow-x-auto rounded-xl border border-slate-800">
          <table class="w-full text-left text-sm">
            <thead class="bg-slate-900/70 text-xs uppercase tracking-wider text-slate-400">
              <tr><th v-for="header in block.headers" :key="header" class="px-4 py-3 font-semibold">{{ header }}</th></tr>
            </thead>
            <tbody>
              <tr v-for="(row, rowIndex) in block.rows" :key="rowIndex" class="border-t border-slate-800/80">
                <td v-for="(cell, cellIndex) in row" :key="cellIndex" class="px-4 py-3 align-top leading-6" :class="cellIndex === 0 ? 'font-mono text-xs text-blue-300' : 'text-slate-300'">{{ cell }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-else-if="block.kind === 'code'" class="mt-4">
          <CopyBlock :text="block.text" :label="block.label" />
        </div>

        <el-alert v-else-if="block.kind === 'warning'" class="mt-4 !rounded-xl" type="warning" show-icon :closable="false" :title="block.title">
          <p class="text-xs leading-6">{{ block.text }}</p>
        </el-alert>

        <p v-else-if="block.kind === 'note'" class="mt-4 rounded-xl border border-slate-800 bg-slate-900/50 px-4 py-3 text-xs leading-6 text-slate-400">
          {{ block.text }}
        </p>
      </template>
    </article>

    <p class="px-1 text-xs leading-6 text-slate-500">
      更完整的手册（BEVFusion 端到端、性能诊断、管理员与运维）在代码仓库的 <code>docs/</code> 目录。
      本页只保留每次提交都用得上的部分，因此可以整页读完。
    </p>
  </section>
</template>

<script setup>
import { nextTick, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'

import CopyBlock from '../../components/CopyBlock.vue'
import { helpSections, renderHelpMarkdown } from '../../help/content'
import { saveBlobAsFile } from '../../checkpointDownload'

const downloading = ref(false)

// The file is built from the same sections rendered above rather than fetched,
// so it always matches what the user just read and needs no server round trip.
function download() {
  downloading.value = true
  try {
    const markdown = renderHelpMarkdown()
    saveBlobAsFile(new Blob([markdown], { type: 'text/markdown;charset=utf-8' }), 'raytrain-使用说明.md')
  } catch (error) {
    ElMessage.error(error.message || '生成文档失败')
  } finally {
    downloading.value = false
  }
}

function scrollTo(id) {
  document.getElementById(`help-${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// Links elsewhere in the app point at a section (/help#contract). Vue Router
// does not scroll to a hash on its own, and the ids carry a prefix so they
// cannot collide with other elements, so resolve it here after the first paint.
const route = useRoute()
onMounted(async () => {
  const target = route.hash.replace(/^#/, '')
  if (!target) return
  await nextTick()
  scrollTo(target)
})
</script>
