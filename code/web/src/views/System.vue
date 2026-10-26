<script setup lang="ts">
import { ref } from 'vue'
import { isDesktopShell, saveDesktopBackup, getDesktopStartup, setDesktopStartup, type DesktopStartup } from '../desktop'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useRouter } from 'vue-router'
import { useAuth } from '../auth'
import { api, post, rows, message } from '../api'
import LoadError from '../components/LoadError.vue'
import SoftwarePackages from '../components/SoftwarePackages.vue'
import ServerConfiguration from '../components/ServerConfiguration.vue'

const serverConfigKey=ref(0)
const router = useRouter()
const auth = useAuth()
const status = ref<any>(null)
const loading = ref(false)
const error = ref('')
const backupBusy = ref(false)
const restoreBusy = ref(false)
const restoreFile = ref<File | null>(null)
const restoreResult = ref<any>(null)
const commitBusy = ref(false)
const listenPort = ref(8090)
const portBusy = ref(false)
const scanRoots = ref<any[]>([])
const scanRootInput = ref('')
const scanRootBusy = ref(false)
const stdioBusy = ref(false)
const catalogRegistries = ref<{ name: string, url: string }[]>([])
const registryResults = ref<any[]>([])
const registryBusy = ref(false)
const startup = ref<DesktopStartup|null>(null)
const startupBusy = ref(false)
const startupError = ref('')
const healthEnabled=ref(true),healthInterval=ref(30),healthBusy=ref(false)
async function saveHealth(){
 healthBusy.value=true
 try{await post('/system/health-check',{enabled:healthEnabled.value,interval_seconds:healthInterval.value});ElMessage.success('健康检查设置已保存')}catch(e){ElMessage.error(message(e))}finally{healthBusy.value=false}
}

async function loadStartup(){
  startupBusy.value=true;startupError.value=''
  try{startup.value=await getDesktopStartup()}catch(e){startupError.value=message(e)}
  finally{startupBusy.value=false}
}
async function saveStartup(enabled:string|number|boolean){
  startupBusy.value=true;startupError.value=''
  try{startup.value=await setDesktopStartup(enabled===true);ElMessage.success(startup.value.enabled?'已开启，下次登录系统时自动启动':'已关闭开机启动')}
  catch(e){startupError.value=message(e)}finally{startupBusy.value=false}
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const requests: Promise<any>[] = [api('/system/status'), api('/system/scan-roots')]
    if (auth.admin) requests.push(api('/system/catalog-registry'))
    const [currentStatus, roots, registry] = await Promise.all(requests)
    status.value = currentStatus
    healthEnabled.value=currentStatus.health_check?.enabled??true
    healthInterval.value=currentStatus.health_check?.interval_seconds??30
    scanRoots.value = rows(roots)
    listenPort.value = status.value.listen_port || 8090
    catalogRegistries.value = registry?.registries || [{ name: 'MCP Registry', url: registry?.registry || 'https://registry.modelcontextprotocol.io' }]
  }
  catch (e) { error.value = message(e) }
  finally { loading.value = false }
}

async function saveRegistry() {
  registryBusy.value = true
  error.value = ''
  try {
    const result = await post<any>('/system/catalog-registry', { registries: catalogRegistries.value })
    catalogRegistries.value = result.registries
    registryResults.value = result.sources || []
    if (registryResults.value.some(s => s.error)) ElMessage.warning('仓库已保存，部分来源检测失败，请查看各源状态')
    else ElMessage.success('所有目录仓库已保存并检测通过')
  } catch (e) { error.value = message(e) }
  finally { registryBusy.value = false }
}

async function addScanRoot() {
  const path = scanRootInput.value.trim()
  if (!path) { error.value = '请输入配置目录的绝对路径'; return }
  scanRootBusy.value = true
  error.value = ''
  try {
    await post('/system/scan-roots', { path })
    scanRoots.value = rows(await api('/system/scan-roots'))
    scanRootInput.value = ''
    ElMessage.success('扫描目录已保存，下次扫描自动包含此目录')
  } catch (e) { error.value = message(e) }
  finally { scanRootBusy.value = false }
}

async function removeScanRoot(path: string) {
  scanRootBusy.value = true
  error.value = ''
  try {
    await api('/system/scan-roots', { method: 'DELETE', body: JSON.stringify({ path }) })
    scanRoots.value = rows(await api('/system/scan-roots'))
    ElMessage.success('扫描目录已移除')
  } catch (e) { error.value = message(e) }
  finally { scanRootBusy.value = false }
}

async function changeStdio() {
  const enabled = !status.value?.environment?.native_stdio
  if (enabled) {
    try {
      await ElMessageBox.confirm('启用后，管理员启动的 stdio MCP 命令会以当前 Toolbox 服务账户权限在本机执行。请仅启动来源可信的配置。', '启用本机 stdio', { type: 'warning', confirmButtonText: '确认启用', cancelButtonText: '取消' })
    } catch { return }
  }
  stdioBusy.value = true
  error.value = ''
  try {
    await post('/system/native-stdio', { enabled })
    status.value = await api('/system/status')
    ElMessage.success(enabled ? '已启用本机 stdio，可在实例页面启动服务' : '已关闭本机 stdio')
  } catch (e) { error.value = message(e) }
  finally { stdioBusy.value = false }
}

async function savePort() {
  const port = Number(listenPort.value)
  if (!Number.isInteger(port) || port < 1024 || port > 65535) {
    error.value = '端口须在 1024–65535 之间'
    return
  }
  if (port === status.value?.listen_port) return
  try {
    await ElMessageBox.confirm(
      isDesktopShell()?`保存后本机 Gateway 将切换到 ${port} 端口，桌面窗口继续使用。MCP 客户端需要更新接入地址。`:`保存后服务将切换到 ${port} 端口，当前页面会跳转。使用旧地址的 MCP 客户端也需要更新接入地址。`,
      '修改监听端口', { type: 'warning', confirmButtonText: '保存并切换', cancelButtonText: '取消' },
    )
  } catch { return }
  portBusy.value = true
  error.value = ''
  try {
    const result = await post<{ url: string }>('/system/listener', { port })
    if(isDesktopShell()) await load(); else window.location.assign(`${result.url}/system`)
  } catch (e) { error.value = message(e) }
  finally { portBusy.value = false }
}

async function downloadBackup() {
  backupBusy.value = true
  error.value = ''
  try {
    if(isDesktopShell()) {
      if(await saveDesktopBackup()) ElMessage.success('备份已保存，请妥善保管备份文件和 MASTER_KEY')
      return
    }
    const filename = 'toolbox-backup.sqlite'
    const target = typeof (window as any).showSaveFilePicker === 'function'
      ? await (window as any).showSaveFilePicker({ suggestedName: filename, types: [{ description: 'SQLite 数据库', accept: { 'application/octet-stream': ['.sqlite'] } }] })
      : null
    const response = await fetch('/api/v1/system/backup', { method: 'POST', credentials: 'same-origin' })
    if (!response.ok) {
      const result = await response.json().catch(() => ({}))
      throw new Error(result.error || `备份失败 (${response.status})`)
    }
    if (target && response.body) {
      await response.body.pipeTo(await target.createWritable())
    } else {
      const url = URL.createObjectURL(await response.blob())
      const link = document.createElement('a')
      link.href = url
      link.download = filename
      document.body.append(link)
      link.click()
      link.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 60000)
    }
    ElMessage.success('备份下载成功，请妥善保存备份文件和 MASTER_KEY')
  } catch (e) { error.value = message(e) }
  finally { backupBusy.value = false }
}

function chooseFile(event: Event) {
  restoreFile.value = (event.target as HTMLInputElement).files?.[0] || null
  restoreResult.value = null
}
async function validateRestore() {
  if (!restoreFile.value) return
  restoreBusy.value = true
  error.value = ''
  try {
    const body = new FormData()
    body.append('file', restoreFile.value)
    const response = await fetch('/api/v1/system/restore', { method: 'POST', credentials: 'same-origin', body })
    const result = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(result.error || `校验失败 (${response.status})`)
    restoreResult.value = result
    ElMessage.success('备份已校验，请核对内容后确认恢复')
  } catch (e) { error.value = message(e) }
  finally { restoreBusy.value = false }
}
async function commitRestore() {
  const candidate = restoreResult.value
  if (!candidate?.restore_id || commitBusy.value) return
  const counts = candidate.counts || {}
  try {
    await ElMessageBox.confirm(
      `确认用此备份替换当前数据库？备份含 ${counts.users ?? 0} 个用户、${counts.instances ?? 0} 个实例、${counts.rules ?? 0} 条规则。账号、授权和 Token 都将回到备份时的状态，过去已撤销的 Token 可能重新有效。当前连接会中断；服务会先保存可回退的旧数据库。`,
      '确认恢复数据库', { type: 'warning', confirmButtonText: '执行恢复', cancelButtonText: '取消' },
    )
  } catch { return }
  commitBusy.value = true
  error.value = ''
  try {
    await post(`/system/restore/${candidate.restore_id}/commit`, { confirm: 'RESTORE' })
    restoreResult.value = null
    restoreFile.value = null
    auth.user = null
    auth.ready = false
    ElMessage.success('数据库已恢复，请使用备份中的账号重新登录并检查 Token 授权')
    await router.replace('/login')
  } catch (e) { error.value = message(e) }
  finally { commitBusy.value = false }
}
load()
if(isDesktopShell()) loadStartup()
</script>
<template>
  <template v-if="auth.admin&&!auth.personal"><SoftwarePackages @directory-changed="serverConfigKey++"/><ServerConfiguration :key="serverConfigKey"/></template>
  <LoadError :error="error" @retry="load" />
  <section v-if="auth.personal" class="panel"><div class="panel-toolbar"><div><h2>开机启动</h2><p>登录系统时自动启动个人版，无需打开浏览器或手动运行程序。</p></div><el-switch :model-value="startup?.enabled||false" :loading="startupBusy" :disabled="!startup?.supported||startupBusy" aria-label="登录系统时自动启动" active-text="已开启" inactive-text="已关闭" @change="saveStartup"/></div><LoadError :error="startupError" @retry="loadStartup"/><p class="muted">{{startup?.message||'正在读取本机启动设置…'}} 开启时会自动保存一份应用到本地数据目录，避免下载临时路径失效。关闭此开关不会退出当前应用。</p></section>
  <section class="panel" v-loading="loading">
    <div class="panel-toolbar"><div><h2>运行环境</h2><p>状态来自当前 Toolbox 服务。</p></div><el-button @click="load">刷新</el-button></div>
    <el-descriptions v-if="status" :column="1" border>
      <el-descriptions-item label="存储">{{status.database||'未知'}}</el-descriptions-item>
      <el-descriptions-item label="数据库文件">{{status.database_file||'未知'}} · Schema {{status.schema_version??'未知'}}</el-descriptions-item>
      <el-descriptions-item label="WAL">{{status.wal?'已启用':'未启用或未知'}}</el-descriptions-item>
      <el-descriptions-item label="运行实例">{{status.runtime?.ready??0}}</el-descriptions-item>
      <el-descriptions-item label="停止实例">{{status.runtime?.stopped??0}}</el-descriptions-item>
      <el-descriptions-item label="故障 / 隔离">{{(status.runtime?.fault??0)+(status.runtime?.quarantined??0)}}</el-descriptions-item>
    </el-descriptions>
  </section>
  <section class="panel" v-if="status"><div class="panel-toolbar"><div><h2>服务端口</h2><p>本机监听地址；修改后页面和 MCP 客户端需使用新端口。</p></div></div><div class="inline-actions"><el-input-number v-model="listenPort" :min="1024" :max="65535" :step="1" :disabled="!status.port_editable || portBusy" /><el-button type="primary" :loading="portBusy" :disabled="!status.port_editable || listenPort === status.listen_port" @click="savePort">保存并切换</el-button></div><p v-if="!status.port_editable" class="muted">当前端口由启动参数或外部部署配置管理，不能在页面修改。</p><p v-else class="muted">当前端口：{{status.listen_port}}。配置保存在服务数据目录，重启后继续生效。</p></section>
  <section class="panel"><div class="panel-toolbar"><div><h2>扫描目录</h2><p>添加 Toolbox 服务所在电脑的 MCP 配置目录；保存后自动纳入“扫描本机配置”，重启后仍有效。</p></div></div><div class="inline-actions"><el-input v-model="scanRootInput" placeholder="例如 /Users/用户名/自定义 MCP 配置" :disabled="scanRootBusy" @keyup.enter="addScanRoot"/><el-button type="primary" :loading="scanRootBusy" @click="addScanRoot">添加目录</el-button></div><p class="muted">只读取目录中的 MCP JSON/TOML 配置，不会运行或纳管服务。请只添加你信任的目录。</p><el-table :data="scanRoots" size="small"><el-table-column prop="path" label="目录" min-width="300" show-overflow-tooltip/><el-table-column label="状态" width="120"><template #default="{row}">{{row.available?'可读取':'目录不可用'}}</template></el-table-column><el-table-column label="来源" width="120"><template #default="{row}">{{row.managed?'页面添加':'启动配置'}}</template></el-table-column><el-table-column label="操作" width="100"><template #default="{row}"><el-button v-if="row.managed" text type="danger" :disabled="scanRootBusy" @click="removeScanRoot(row.path)">移除</el-button></template></el-table-column><template #empty><el-empty description="尚未添加扫描目录" :image-size="50"/></template></el-table></section>
  <section class="panel" v-if="auth.admin">
    <div class="panel-toolbar"><div><h2>目录仓库</h2><p>支持多个兼容官方 MCP Registry v0.1 API 的 HTTPS 仓库；GitHub 项目地址、npm 镜像地址不能作为目录仓库。</p></div><el-button :disabled="registryBusy || catalogRegistries.length >= 8" @click="catalogRegistries.push({name:'',url:''})">添加仓库</el-button></div>
    <el-table :data="catalogRegistries">
      <el-table-column label="名称" width="220"><template #default="{row}"><el-input v-model="row.name" placeholder="仓库名称" :disabled="registryBusy"/></template></el-table-column>
      <el-table-column label="API 根地址" min-width="320"><template #default="{row}"><el-input v-model="row.url" placeholder="输入 HTTPS API 根地址" :disabled="registryBusy"/></template></el-table-column>
      <el-table-column label="操作" width="100"><template #default="{row}"><el-button :disabled="registryBusy" @click="catalogRegistries.splice(catalogRegistries.indexOf(row),1)">移除</el-button></template></el-table-column>
    </el-table>
    <el-button type="primary" :loading="registryBusy" @click="saveRegistry">保存并检测全部仓库</el-button>
    <p class="muted">最多 8 个来源。清空列表后保存恢复官方目录。搜索会并行查询各仓库，某个来源不可用时仍显示其他来源结果。</p>
    <el-alert v-for="source in registryResults" :key="source.url" :type="source.error ? 'warning' : 'success'" :title="source.name + '：' + (source.error || 'API 检测通过')" :description="source.url" :closable="false" style="margin-top:8px"/>
  </section>
<section class="panel" v-if="auth.admin&&status"><div class="panel-toolbar"><div><h2>自动健康检查</h2><p>定时检测 MCP 连接，故障实例恢复后自动更新为可用。</p></div></div><div class="inline-actions"><el-switch v-model="healthEnabled" aria-label="启用自动健康检查"/><span>检测间隔（秒）</span><el-input-number v-model="healthInterval" :min="10" :max="3600" :step="10" aria-label="健康检查间隔"/><el-button :loading="healthBusy" @click="saveHealth">保存健康检查设置</el-button></div><p class="muted">默认每 30 秒检测，故障连接自动重试；PostgreSQL 使用只读查询验证。手动停止及结果未知的隔离实例不自动启动，不会重放业务调用。</p></section>
<section class="panel" v-if="status?.environment"><div class="panel-toolbar"><div><h2>本机运行环境</h2><p>安装及 stdio 服务会在 Toolbox 所在电脑运行。</p></div></div><el-descriptions :column="1" border><el-descriptions-item v-for="name in ['node','npx','python','uv','uvx']" :key="name" :label="name">{{status.environment[name]?.available?(status.environment[name]?.version||'已安装'):'未找到'}}</el-descriptions-item><el-descriptions-item label="本机 stdio">{{status.environment.native_stdio?'已允许':'未启用'}}</el-descriptions-item></el-descriptions><div class="inline-actions"><el-button :loading="stdioBusy" :disabled="!status.environment.native_stdio_editable" @click="changeStdio">{{status.environment.native_stdio?'关闭本机 stdio':'启用本机 stdio'}}</el-button><span v-if="!status.environment.native_stdio_editable" class="muted">当前由部署配置管理。</span></div><p class="muted">本机 stdio 以 Toolbox 服务账户运行命令；关闭前请先在实例页面停止所有 stdio 服务。设置保存在 SQLite 中，重启后继续生效。</p></section>
  <section class="panel"><div class="panel-toolbar"><div><h2>SQLite 备份</h2><p>下载数据库一致性快照。恢复时还需要原来的 MASTER_KEY 才能解密凭据。</p></div><el-button type="primary" :loading="backupBusy" @click="downloadBackup">下载备份</el-button></div></section>
  <section class="panel"><div class="panel-toolbar"><div><h2>恢复数据库</h2><p>先上传备份并校验结构与密钥，再确认切换。切换时会中断连接；旧数据库留在服务数据目录用于回退。</p></div></div><div class="inline-actions"><input type="file" accept=".sqlite,.db,application/octet-stream" @change="chooseFile"/><el-button :disabled="!restoreFile" :loading="restoreBusy" @click="validateRestore">上传并校验</el-button></div><template v-if="restoreResult"><p class="muted">{{restoreResult.message}} 校验结果 30 分钟内有效。</p><el-descriptions :column="1" border><el-descriptions-item label="用户">{{restoreResult.counts?.users??0}}</el-descriptions-item><el-descriptions-item label="实例">{{restoreResult.counts?.instances??0}}</el-descriptions-item><el-descriptions-item label="授权规则">{{restoreResult.counts?.rules??0}}</el-descriptions-item><el-descriptions-item label="调用日志">{{restoreResult.counts?.logs??0}}</el-descriptions-item></el-descriptions><div class="inline-actions"><el-button type="danger" :loading="commitBusy" @click="commitRestore">确认恢复数据库</el-button></div></template></section>
</template>
