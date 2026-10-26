<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Connection, Monitor, Search, Tickets, Lock, Share, User, SwitchButton, Setting } from '@element-plus/icons-vue'
import { useAuth } from './auth'
import { ElMessage } from 'element-plus'
import { message } from './api'
import { isDesktopShell } from './desktop'
import BootSkeleton from './components/BootSkeleton.vue'
const route = useRoute(), router = useRouter(), auth = useAuth()
const links = [ ['/instances','MCP 管理',Connection],['/clients','客户端',Monitor],['/catalog','MCP市场',Search],['/logs','调用日志',Tickets],['/rules','安全策略',Lock],['/gateway','Gateway',Share],['/users','用户管理',User],['/system-logs','系统日志',Tickets],['/system','系统设置',Setting] ] as const
async function logout() { try { await auth.logout(); router.replace('/login') } catch (e) { ElMessage.error(message(e)) } }
function expired() { auth.user = null; if(auth.personal) { ElMessage.error('本地服务暂不可用，请重试或重新启动应用'); return }; router.replace('/login'); ElMessage.warning('登录已失效，请重新登录') }
async function retryLocal(){ try{await auth.refresh()}catch(e){ElMessage.error(message(e))} }
onMounted(() => window.addEventListener('session-expired', expired))
onUnmounted(() => window.removeEventListener('session-expired', expired))
</script>
<template>
  <BootSkeleton v-if="auth.personal&&(!auth.ready||auth.loading)&&route.path!=='/download'"/>
  <router-view v-else-if="['/login','/change-password','/download'].includes(route.path)" />
  <div v-else-if="auth.personal&&!auth.user" class="panel" style="margin:48px"><h2>本地服务暂不可用</h2><p>请检查应用数据目录中的 toolbox.log，或重新启动应用。</p><el-button type="primary" @click="retryLocal">重新连接</el-button></div>
  <div v-else class="app-shell">
    <aside class="sidebar">
      <a href="/instances" class="brand"><div>Toolbox<small>MCP WORKSPACE</small></div></a>
      <div class="nav-label">工作空间</div>
      <nav><template v-for="[path,label,icon] in links" :key="path"><router-link v-if="(!auth.personal||path!=='/users')&&(!['/rules','/users','/system','/system-logs'].includes(path) || auth.admin)" :to="path" :class="{active: route.path === path}"><el-icon><component :is="icon" /></el-icon>{{ label }}<span v-if="route.path === path" class="nav-dot" /></router-link></template></nav>
      <div class="sidebar-foot"><span class="secure-dot" /> {{auth.personal?'个人本地版':'组织级访问控制'}}<small>{{auth.personal?'本机独立运行，全功能管理':'按需授权，集中管理'}}</small></div>
    </aside>
    <div class="workspace-main">
      <header class="topbar"><span class="breadcrumb">工作空间 <span>/</span> <strong>{{ route.meta.title }}</strong></span><div class="identity"><router-link v-if="!isDesktopShell()" to="/download">下载桌面版</router-link><el-tag v-if="!auth.personal" type="info" effect="plain" round>{{ auth.admin ? '管理员' : '只读成员' }}</el-tag><template v-if="!auth.personal"><span class="avatar">{{ auth.user?.username?.slice(0,1).toUpperCase() }}</span><span>{{ auth.user?.username }}</span></template><el-button v-if="!auth.personal" text circle aria-label="退出登录" @click="logout"><el-icon><SwitchButton /></el-icon></el-button></div></header>
      <main class="main-content"><div class="page-title"><div><div class="eyebrow">TOOLBOX / WORKSPACE</div><h1>{{ route.meta.title }}</h1><p>{{ route.meta.description }}</p></div><span class="version-pill">TOOLBOX MCP</span></div><router-view /></main>
      <footer class="page-footer">Toolbox<span>统一接入 · 清晰授权 · 可追踪调用</span></footer>
    </div>
  </div>
</template>
