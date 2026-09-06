<template>
  <section class="mt-3 rounded-xl border border-slate-700 p-4 text-xs text-slate-300" aria-label="镜像环境详情">
    <p class="font-semibold text-white">{{ image.name }} · 环境说明</p>
    <p class="mt-2 text-amber-300">管理员声明 / 未经平台自动验证。CPU 检查不代表 GPU 或多卡训练已验证。</p>
    <p class="mt-2 whitespace-pre-wrap break-words">{{ image.description || '描述：未提供' }}</p>
    <dl class="mt-3 grid gap-3 sm:grid-cols-2">
      <div v-for="row in rows" :key="row.key">
        <dt class="text-slate-400">{{ row.label }}</dt>
        <dd class="mt-1 whitespace-pre-wrap break-words">{{ row.value }}</dd>
      </div>
    </dl>
    <p class="mt-3 break-all font-mono">{{ image.reference }}</p>
  </section>
</template>

<script setup>
import { computed } from 'vue'
import { imageEnvironmentRows } from '../imageEnvironment'
const props = defineProps({ image: { type: Object, required: true } })
const rows = computed(() => imageEnvironmentRows(props.image))
</script>
