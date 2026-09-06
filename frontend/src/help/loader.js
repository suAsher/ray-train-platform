import { ref } from 'vue'

export function createDocumentLoader(fetchDocuments) {
  const documents = ref([]), loading = ref(true), error = ref('')
  let request = 0
  async function load() {
    const current = ++request
    loading.value = true; error.value = ''
    try {
      const result = await fetchDocuments()
      if (current === request) documents.value = result.items || []
    } catch (err) {
      if (current === request) { documents.value = []; error.value = err.message || '文档加载失败' }
    } finally {
      if (current === request) loading.value = false
    }
  }
  return { documents, loading, error, load }
}
