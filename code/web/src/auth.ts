import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api, post, ApiError } from './api'
import { isDesktopShell } from './desktop'
export interface User { id: string; username: string; role: string; enabled: boolean; must_change_password: boolean; personal?:boolean }
export const useAuth = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const ready = ref(false)
  const loading = ref(false)
  const admin = computed(() => user.value?.role === 'admin')
  const personal = isDesktopShell()
  async function refresh() { loading.value=true;try { user.value = await api<User>('/me') } catch (e) { if (!(e instanceof ApiError) || e.status !== 401) throw e; user.value = null } finally { ready.value = true;loading.value=false } }
  async function logout() { await post('/logout'); user.value = null }
  return { user, ready, loading, admin, personal, refresh, logout }
})
