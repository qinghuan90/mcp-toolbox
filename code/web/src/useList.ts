import { onMounted, ref } from 'vue'
import { api, message, rows } from './api'
export function useList(path: string, immediate = true) {
  const items = ref<any[]>([]), loading = ref(immediate), error = ref(''), total = ref(0)
  let sequence = 0
  async function load(query = '') { const id = ++sequence; loading.value = true; error.value = ''; try { const data = await api(path + query); if(id !== sequence) return; items.value = rows(data); total.value = data?.total ?? items.value.length } catch(e) { if(id === sequence) error.value = message(e) } finally { if(id === sequence) loading.value = false } }
  if (immediate) onMounted(() => load())
  return { items, loading, error, total, load }
}
