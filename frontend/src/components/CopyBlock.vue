<template>
  <div class="copy-block group">
    <div v-if="label" class="mb-2 flex items-center justify-between gap-3">
      <p class="text-xs font-semibold text-slate-300">{{ label }}</p>
      <el-button size="small" :type="copied ? 'success' : 'default'" :icon="copied ? 'Select' : 'DocumentCopy'" @click="copy">
        {{ copied ? '已复制' : '复制' }}
      </el-button>
    </div>
    <div class="relative">
      <pre class="copy-block__code" :class="wrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'">{{ text }}</pre>
      <el-button
        v-if="!label"
        class="copy-block__floating"
        size="small"
        :type="copied ? 'success' : 'default'"
        :icon="copied ? 'Select' : 'DocumentCopy'"
        @click="copy"
      >
        {{ copied ? '已复制' : '复制' }}
      </el-button>
    </div>
    <p v-if="copyError" class="mt-2 text-[11px] text-amber-300">{{ copyError }}</p>
  </div>
</template>

<script setup>
import { ref } from 'vue'

import { copyToClipboard } from '../clipboard'

const props = defineProps({
  text: { type: String, required: true },
  label: { type: String, default: '' },
  wrap: { type: Boolean, default: false },
})

const copied = ref(false)
const copyError = ref('')
let resetTimer

const copy = async () => {
  copyError.value = ''
  if (await copyToClipboard(props.text)) {
    copied.value = true
    window.clearTimeout(resetTimer)
    resetTimer = window.setTimeout(() => { copied.value = false }, 1800)
    return
  }
  copyError.value = '浏览器阻止了剪贴板访问，请手动选中上方文本复制。'
}
</script>

<style scoped>
.copy-block__code {
  overflow-x: auto;
  border: 1px solid rgb(30 41 59);
  border-radius: 0.75rem;
  background: rgb(2 6 23 / 0.85);
  padding: 1rem;
  padding-right: 5.5rem;
  color: rgb(191 219 254);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.75rem;
  line-height: 1.75;
  margin: 0;
}

.copy-block__floating {
  position: absolute;
  top: 0.6rem;
  right: 0.6rem;
  opacity: 0;
  transition: opacity 0.15s ease;
}

.group:hover .copy-block__floating,
.copy-block__floating:focus-visible {
  opacity: 1;
}

/* Touch devices have no hover, so the control must stay visible there. */
@media (hover: none) {
  .copy-block__floating {
    opacity: 1;
  }
}
</style>
