<template><div class="help-markdown" @click="act" v-html="rendered.html" /></template>
<script setup>
import { computed } from 'vue'
import { ElMessage } from 'element-plus'
import { renderDocumentMarkdown } from '../help/documents.js'
import { saveBlobAsFile } from '../checkpointDownload.js'
const props = defineProps({ markdown: { type: String, default: '' } })
const rendered = computed(() => {
  const env = { codes: [] }
  const html = renderDocumentMarkdown(props.markdown, env)
  return { html, codes: env.codes }
})
async function act(event) {
  const button = event.target.closest('button')
  if (!button || !event.currentTarget.contains(button)) return
  try {
    const download = button.dataset.helpDownload
    const index = Number(download ?? button.dataset.helpCopy)
    const text = rendered.value.codes[index]
    if (!Number.isInteger(index) || typeof text !== 'string') return
    if (download !== undefined) saveBlobAsFile(new Blob([text], { type: 'text/plain;charset=utf-8' }), button.dataset.helpFilename)
    else { await navigator.clipboard.writeText(text); ElMessage.success('代码已复制') }
  } catch (error) { ElMessage.error(error.message || '操作失败') }
}
</script>
<style scoped>
.help-markdown { overflow-wrap:anywhere; color:#cbd5e1; font-size:14px; line-height:1.8 }
.help-markdown :deep(h1),.help-markdown :deep(h2),.help-markdown :deep(h3),.help-markdown :deep(h4) { color:#f1f5f9; font-weight:600; margin:1.4em 0 .6em }
.help-markdown :deep(h1) { font-size:24px }.help-markdown :deep(h2) {font-size:20px}.help-markdown :deep(h3) {font-size:17px}
.help-markdown :deep(p) {margin:.8em 0}.help-markdown :deep(ul) {list-style:disc;padding-left:1.6em}.help-markdown :deep(ol) {list-style:decimal;padding-left:1.6em}
.help-markdown :deep(pre) {overflow-x:auto; padding:16px; background:#080f20; border-radius:8px; white-space:pre;line-height:1.6}
.help-markdown :deep(code) {font-family:monospace;color:#93c5fd}.help-markdown :deep(blockquote) {border-left:3px solid #64748b;padding-left:16px}
.help-markdown :deep(table) {display:block;overflow:auto;border-collapse:collapse;margin:16px 0}.help-markdown :deep(th),.help-markdown :deep(td) {border:1px solid #334155;padding:8px 12px}
.help-markdown :deep(a) {color:#60a5fa;text-decoration:underline}.help-markdown :deep(button) {color:#93c5fd;border:1px solid #334155;border-radius:5px;padding:3px 10px;margin:6px 8px 8px 0}
</style>
