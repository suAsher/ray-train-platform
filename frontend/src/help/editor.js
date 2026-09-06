import { computed, ref } from 'vue'

const empty = () => ({ id: '', title: '', category: '其他', sortOrder: 0, markdown: '', version: 0, publishedVersion: 0 })
const fields = doc => ({ title: doc.title, category: doc.category, sortOrder: doc.sortOrder, markdown: doc.markdown })
const signature = doc => JSON.stringify({ id: doc.id, ...fields(doc) })
export function createDocumentEditor(api) {
  const form = ref(empty()), baseline = ref(signature(form.value))
  const dirty = computed(() => signature(form.value) !== baseline.value)
  function select(doc) { form.value = doc ? { ...doc } : empty(); baseline.value = signature(form.value) }
  function validate() {
    const doc = form.value
    if (!/^[a-z0-9][a-z0-9-]{0,79}$/.test(doc.id)) throw new Error('ID 必须是 1–80 位小写字母、数字或连字符')
    if (!doc.title.trim() || [...doc.title].length > 200) throw new Error('标题必填，最多 200 字')
    if (!doc.category.trim() || [...doc.category].length > 100) throw new Error('分类必填，最多 100 字')
    if (!doc.markdown.trim() || new TextEncoder().encode(doc.markdown).length > 512 * 1024) throw new Error('正文必填，最多 512 KiB')
    if (!Number.isInteger(doc.sortOrder) || Math.abs(doc.sortOrder) > 100000) throw new Error('排序必须为 -100000 到 100000 之间的整数')
  }
  async function save() {
    validate()
    const doc = form.value
    const next = doc.version ? await api.saveHelpDocument(doc.id, { ...fields(doc), expectedVersion: doc.version }) : await api.createHelpDocument({ id: doc.id, ...fields(doc) })
    select(next); return next
  }
  async function publish() {
    if (dirty.value || !form.value.version) throw new Error('请先保存草稿，再发布')
    const next = await api.publishHelpDocument(form.value.id, form.value.version)
    select(next); return next
  }
  async function unpublish() {
    if (dirty.value) throw new Error('请先保存草稿或放弃修改')
    const next = await api.unpublishHelpDocument(form.value.id, form.value.version)
    select(next); return next
  }
  async function restore(version) {
    if (dirty.value) throw new Error('请先保存草稿或放弃修改')
    const next = await api.restoreHelpDocument(form.value.id, form.value.version, version)
    select(next); return next
  }
  return { form, dirty, select, save, publish, unpublish, restore }
}
