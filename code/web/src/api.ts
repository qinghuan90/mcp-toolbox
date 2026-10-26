export class ApiError extends Error { constructor(message: string, public status: number) { super(message) } }
export async function api<T = any>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...options, credentials: 'same-origin', headers: { ...(options.body && !(options.body instanceof FormData) ? { 'Content-Type': 'application/json' } : {}), ...options.headers },
  })
  const body = await response.text()
  let data: any
  try { data = body ? JSON.parse(body) : undefined } catch { throw new ApiError('服务响应格式异常，请检查后端是否启动。', response.status) }
  if (!response.ok) {
    if (response.status === 401 && !['/login','/setup','/me'].includes(path)) window.dispatchEvent(new Event('session-expired'))
    throw new ApiError(data?.error || `请求失败 (${response.status})`, response.status)
  }
  return data as T
}
export const post = <T = any>(path: string, data: unknown = {}) => api<T>(path, { method: 'POST', body: JSON.stringify(data) })
export const patch = <T = any>(path: string, data: unknown) => api<T>(path, { method: 'PATCH', body: JSON.stringify(data) })
export const remove = (path: string) => api(path, { method: 'DELETE' })
export function rows(data: any): any[] { return Array.isArray(data) ? data : (data?.items || []) }
export function date(value: any): string { return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—' }
export const message = (error: unknown) => error instanceof Error ? error.message : '操作失败，请稍后重试'
export async function clipboard(value: string) {
  if (!navigator.clipboard) throw new Error('当前浏览器不支持自动复制，请手动复制。')
  await navigator.clipboard.writeText(value)
}
