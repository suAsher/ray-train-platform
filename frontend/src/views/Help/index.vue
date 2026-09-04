<template>
  <div class="space-y-6">
    <section class="rounded-2xl border border-blue-500/20 bg-gradient-to-br from-blue-950/40 to-[#131826] p-7 shadow-xl">
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
    </section>

    <div class="grid gap-6 xl:grid-cols-[16rem_minmax(0,1fr)]">
      <nav class="panel h-fit p-3 xl:sticky xl:top-4">
        <div v-for="group in groupedSections" :key="group.name" class="mb-3 last:mb-0">
          <p class="px-3 pb-1 text-[11px] font-semibold uppercase tracking-wider text-slate-500">{{ group.name }}</p>
          <button
            v-for="section in group.sections"
            :key="section.id"
            type="button"
            class="block w-full rounded-lg px-3 py-2 text-left text-sm leading-5 transition"
            :class="section.id === activeId ? 'bg-blue-500/15 font-semibold text-blue-200' : 'text-slate-400 hover:bg-slate-800/60 hover:text-slate-200'"
            @click="select(section.id)"
          >{{ section.title }}</button>
        </div>
      </nav>

      <section v-for="section in [activeSection]" :key="section.id" class="panel p-6">
      <h4 class="text-lg font-bold text-white">{{ section.title }}</h4>
      <p v-if="section.summary" class="mt-2 max-w-3xl text-sm leading-6 text-slate-400">{{ section.summary }}</p>

      <template v-for="(block, index) in section.blocks" :key="index">
        <ol v-if="block.kind === 'steps'" class="mt-5 space-y-6">
          <li v-for="(item, step) in block.items" :key="item.title" class="flex gap-4">
            <span class="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-blue-500/15 font-mono text-xs font-semibold text-blue-300">{{ step + 1 }}</span>
            <div class="min-w-0 flex-1">
              <p class="text-sm font-semibold text-slate-100">{{ item.title }}</p>
              <p class="mt-1 max-w-3xl text-sm leading-6 text-slate-400">{{ item.body }}</p>
              <CopyBlock v-if="item.code" class="mt-3" :text="item.code" :label="item.codeLabel" />
            </div>
          </li>
        </ol>

        <ul v-else-if="block.kind === 'list'" class="mt-4 space-y-2">
          <li v-for="item in block.items" :key="item" class="flex max-w-4xl gap-2 text-sm leading-6 text-slate-300">
            <span class="text-blue-400">·</span><span>{{ item }}</span>
          </li>
        </ul>

        <ul v-else-if="block.kind === 'checklist'" class="mt-4 space-y-2">
          <li v-for="item in block.items" :key="item" class="flex max-w-4xl gap-2 text-sm leading-6 text-slate-300">
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
      </section>
    </div>

    <p class="px-1 text-xs leading-6 text-slate-500">
      这一页只放每次提交都用得上的部分，所以可以整页读完。
      从自己的机器提交、CLI 的安装与登录见「<router-link class="text-blue-400 hover:text-blue-300" to="/external-submit">外部提交</router-link>」；
      命令行的完整参数用 <code>spk-rayjob --help</code> 查看。
      运行环境缺少你需要的依赖时，联系平台管理员登记新镜像 —— 镜像只提供环境，你的代码不进镜像，改完直接重新提交即可。
    </p>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
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

// Twelve topics do not fit one readable scroll, so the page shows the one the
// reader picked. The download still carries all of them, because a file kept on
// a laptop is read differently from a page browsed with a question in mind.
const activeId = ref(helpSections[0].id)

// Seventeen topics in one flat list is a wall of text to scan. Grouping keeps
// each list short enough to read, and the order matches how someone moves
// through the platform: get something running, then data, then training.
const groupedSections = computed(() => {
  const order = []
  const byGroup = new Map()
  for (const section of helpSections) {
    const name = section.group || '其他'
    if (!byGroup.has(name)) {
      byGroup.set(name, [])
      order.push(name)
    }
    byGroup.get(name).push(section)
  }
  return order.map((name) => ({ name, sections: byGroup.get(name) }))
})
const activeSection = computed(
  () => helpSections.find((section) => section.id === activeId.value) || helpSections[0],
)

function select(id) {
  activeId.value = id
  if (route.hash !== `#${id}`) router.replace({ hash: `#${id}` })
}

// Links elsewhere in the app point at a topic (/help#data-mode), so honour the
// hash on arrival instead of always opening the first one.
const route = useRoute()
const router = useRouter()
onMounted(() => {
  const target = route.hash.replace(/^#/, '')
  if (target && helpSections.some((section) => section.id === target)) activeId.value = target
})
</script>
