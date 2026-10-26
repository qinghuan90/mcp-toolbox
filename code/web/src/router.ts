import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from './auth'
import { isDesktopShell } from './desktop'
export const router = createRouter({ history: createWebHistory(), routes: [
  { path: '/download', component: () => import('./views/Download.vue'), meta: { title: '下载桌面版' } },
  { path: '/login', component: () => import('./views/Login.vue'), meta: { title: '登录' } },
  { path: '/change-password', component: () => import('./views/ChangePassword.vue'), meta: { title: '修改初始密码' } },
  { path: '/', redirect: '/instances' },
  { path: '/instances', component: () => import('./views/Instances.vue'), meta: { title: 'MCP 管理', description: '集中配置与运行你的工具服务' } },
  { path: '/clients', component: () => import('./views/Clients.vue'), meta: { title: '客户端', description: '查看客户端接入与最近访问' } },
  { path: '/catalog', component: () => import('./views/Catalog.vue'), meta: { title: 'MCP市场', description: '发现 MCP 服务，支持在线目录和离线安装' } },
  { path: '/logs', component: () => import('./views/Logs.vue'), meta: { title: '调用日志', description: '追踪请求结果、耗时与归属' } },
  { path: '/rules', component: () => import('./views/Rules.vue'), meta: { title: '安全策略', description: '按客户端来源 IP 管理组织接入规则', admin: true } },
  { path: '/gateway', component: () => import('./views/Gateway.vue'), meta: { title: 'Gateway', description: '一个接入地址，连接你的 AI 客户端' } },
  { path: '/users', component: () => import('./views/Users.vue'), meta: { title: '用户管理', description: '管理账号、角色与访问状态', admin: true } },
  { path: '/system-logs', component: () => import('./views/SystemLogs.vue'), meta: { title: '系统日志', description: '查看最近 7 天的运行与异常日志', admin: true } },
  { path: '/system', component: () => import('./views/System.vue'), meta: { title: '系统设置', description: '查看运行状态、备份与恢复数据库', admin: true } },
  { path: '/:pathMatch(.*)*', redirect: '/instances' },
] })
router.beforeEach(async to => {
  const auth = useAuth()
  if(isDesktopShell() && ['/login','/change-password','/users'].includes(to.path)) return '/instances'
  if (['/login','/download'].includes(to.path)) return
  try { if (!auth.ready) await auth.refresh() } catch { return isDesktopShell()?true:'/login' }
  if(isDesktopShell()) return true
  if (!auth.user) return '/login'
  if (auth.user.must_change_password && to.path !== '/change-password') return '/change-password'
  if (to.meta.admin && !auth.admin) return '/instances'
})
