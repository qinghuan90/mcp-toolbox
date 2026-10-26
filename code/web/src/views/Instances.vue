<script setup lang="ts">
import MCPOAuthAuthorization from '../components/MCPOAuthAuthorization.vue'
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { Plus, Refresh, Search, Connection, Delete } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useAuth } from '../auth'
import { api, post, patch, remove, message, rows } from '../api'
import { useList } from '../useList'
import LoadError from '../components/LoadError.vue'
import BootSkeleton from '../components/BootSkeleton.vue'
import InstanceHealth from '../components/InstanceHealth.vue'
import ScanImport from '../components/ScanImport.vue'
import PostgresConnections from '../components/PostgresConnections.vue'
import { parseMCPConfig } from '../mcpConfig'
const auth=useAuth(), {items,loading,error,load}=useList('/instances')
const search=ref(''), filter=ref(''), page=ref(1), dialog=ref(false), saving=ref(false), editing=ref<any>(null), operation=ref(''), formError=ref(''), tools=ref<any[]>([]), selected=ref<any>(null), toolsBusy=ref(false), toolsError=ref('')
const oauthInstance=ref<any>(null)
const tagFilter=ref(''), copying=ref(false)
const tagOptions=computed(()=>[...new Set(items.value.map(x=>String(x.account_label||'').trim()).filter(Boolean))].sort((a,b)=>a.localeCompare(b,'zh-CN')))
const empty=()=>({name:'',description:'',account_label:'',transport:'http',url:'',command:'',cwd:'',header_helper:'',concurrency:1,timeout_seconds:60})
const form=reactive(empty())
const headerRows=ref<{key:string,value:string}[]>([]), envRows=ref<{key:string,value:string}[]>([])
const httpAdvanced=ref<string[]>([])
const pgDraft=ref<any>(undefined),pgSupported=ref(false)
const configMode=ref('form'),jsonText=ref(''),pgEditor=ref<InstanceType<typeof PostgresConnections>|null>(null)
const argRows=ref<string[]>([]),configLoading=ref(false),configLoadError=ref(''),pgExpanded=ref<string[]>([])
let loadedArgs:string[]=[]
let loadedEnv:Record<string,string>={}
let loadedHeaders:Record<string,string>={}
let loadedHelper=''
let editorSequence=0
function privateArgument(value:string){return value!==''&&!['-y','--yes'].includes(value)&&!/^@[a-z0-9._-]+\/[a-z0-9._-]+(?:@[\w.*+-]+)?$/i.test(value)}
function resetEditorSecrets(){jsonText.value='';argRows.value=[];envRows.value=[];loadedArgs=[];loadedEnv={};headerRows.value=[];loadedHeaders={};loadedHelper='';form.header_helper='';testResult.value=null;testError.value=''}
const testing=ref(false), testResult=ref<any>(null), testError=ref('')
const filtered=computed(()=>items.value.filter(x=>(!search.value || x.name.toLowerCase().includes(search.value.toLowerCase()))&&(!filter.value||(filter.value==='ready'?x.status==='ready'&&x.health?.protocol?.state==='passed':x.status===filter.value))&&(!tagFilter.value||x.account_label===tagFilter.value)))
const visible=computed(()=>filtered.value.slice(Math.max(0,page.value-1)*10,page.value*10))
const available=computed(()=>items.value.filter(x=>x.status==='ready'&&x.health?.protocol?.state==='passed').length)
const localCount=computed(()=>items.value.filter(x=>x.transport==='stdio').length)
const remoteCount=computed(()=>items.value.filter(x=>x.transport==='http').length)
const effectiveVersion=ref<number|null>(null)
let mounted=true, actionTimer:ReturnType<typeof setTimeout>|undefined, finishWait:(()=>void)|undefined
function pause(){return new Promise<void>(resolve=>{finishWait=resolve;actionTimer=setTimeout(()=>{finishWait=undefined;resolve()},1200)})}
async function edit(item?:any, copy=false) {
  const sequence=++editorSequence
  httpAdvanced.value=[];loadedHeaders={};loadedHelper='';pgExpanded.value=[];pgSupported.value=false;loadedEnv={};configLoadError.value='';configLoading.value=false
  configMode.value='form';jsonText.value='';pgDraft.value=undefined
  copying.value=copy
  editing.value=copy?null:item||null
  Object.assign(form,empty(),item?{name:item.name,description:item.description,account_label:item.account_label,transport:item.transport,url:item.url,command:item.command,cwd:item.cwd,concurrency:item.concurrency,timeout_seconds:item.timeout_seconds}:{})
  if(copy&&item){
   const base=String(item.name).slice(0,85);let name=`${base}-副本`,suffix=2
   while(items.value.some(x=>x.name===name))name=`${base}-副本${suffix++}`
   form.name=name
  }
  argRows.value=[...(item?.args||[])];loadedArgs=[...argRows.value]
  headerRows.value=[]; envRows.value=[]
  formError.value=''; testResult.value=null; testError.value=''
  dialog.value=true
  if(item){
   configLoading.value=true
   try{
    const data=await api(`/instances/${item.id}/editor`)
    if(sequence!==editorSequence||!dialog.value)return
    if(data.version!==item.version)throw new Error('配置已被其他操作更新，请关闭窗口并刷新列表')
    argRows.value=[...(data.args||[])];loadedArgs=[...argRows.value]
    loadedHeaders={...(data.headers||{})}
    headerRows.value=Object.entries(loadedHeaders).map(([key,value])=>({key,value:String(value)}))
    loadedHelper=data.header_helper||'';form.header_helper=loadedHelper
    loadedEnv={...(data.env||{})}
    envRows.value=Object.entries(loadedEnv).map(([key,value])=>({key,value:String(value)}))
   }catch(e){if(sequence===editorSequence)configLoadError.value=message(e)}finally{if(sequence===editorSequence)configLoading.value=false}
  }
}
function rowsToObject(list:{key:string,value:string}[]):Record<string,string>{const out:Record<string,string>=Object.create(null);for(const r of list){const k=r.key.trim();if(!k&&r.value)throw new Error('请填写键名');if(k){if(k in out)throw new Error(`键名重复：${k}`);out[k]=r.value}}return out}
function submitArgs():string[]{return [...argRows.value]}
function buildFormBody(){
  const body:any={name:form.name,description:form.description,account_label:form.account_label,transport:form.transport,url:form.url,command:form.command,cwd:form.cwd,args:submitArgs(),concurrency:form.concurrency,timeout_seconds:form.timeout_seconds}
  if(editing.value)body.version=editing.value.version
  if(form.transport==='http'){
    body.headers=rowsToObject(headerRows.value)
    body.header_helper=form.header_helper
  }else{
    body.env=rowsToObject(envRows.value)
  }
  const connection=pgEditor.value?.getConnection()
  if(connection){
   if(JSON.stringify(argRows.value)!==JSON.stringify(loadedArgs))throw new Error('请分别保存启动参数和数据库辅助配置，避免两种设置互相覆盖')
   body.postgres_connection=connection
  }
  return body
}
function buildBody(){
 if(configLoading.value)throw new Error('正在读取已保存参数，请稍候')
 if(configLoadError.value)throw new Error(configLoadError.value)
 const body=configMode.value==='json'?parseMCPConfig(jsonText.value):buildFormBody()
 if(editing.value){
  body.version=editing.value.version
  if((body.args||[]).some((a:string)=>a.includes('<REDACTED>'))&&JSON.stringify(body.args)!==JSON.stringify(editing.value.args||[]))throw new Error('已脱敏参数不能局部修改，请保留整个 args 数组或填写完整的新参数；修改库名可使用数据库连接表单')
 }
 return body
}
function changeConfigMode(mode:string|number|boolean|undefined){
 try{
  if(mode==='json'){
   if(configLoading.value||pgEditor.value?.loading)throw new Error('正在读取配置，请稍候')
   if(configLoadError.value)throw new Error(configLoadError.value)
   const body=buildFormBody();delete body.version
   jsonText.value=JSON.stringify(body,null,2);configMode.value='json'
  }else{
   const body=parseMCPConfig(jsonText.value,true)
   pgDraft.value=body.postgres_connection
   Object.assign(form,empty(),body)
   argRows.value=[...(body.args||[])]
   headerRows.value=Object.entries(body.headers??loadedHeaders).map(([key,value])=>({key,value:String(value)}))
   envRows.value=Object.entries(body.env??loadedEnv).map(([key,value])=>({key,value:String(value)}))
   form.header_helper=body.header_helper??loadedHelper
   configMode.value='form'
  }
  formError.value='';testResult.value=null;testError.value=''
 }catch(e){formError.value=message(e)}
}
async function discardJSON(){
 try{await ElMessageBox.confirm('放弃 JSON 中尚未应用的修改，恢复切换前的表单？','返回表单',{type:'warning'});configMode.value='form';formError.value=''}catch{}
}
async function save() {
 saving.value=true;formError.value='';const isEdit=!!editing.value
 try{
  if(configMode.value==='form'&&pgEditor.value?.loading)throw new Error('正在读取数据库配置，请稍候')
  const body=buildBody()
  if(!body.name?.trim())throw new Error('请输入实例名称')
  const created=isEdit?await patch(`/instances/${editing.value.id}`,body):await post('/instances',body)
  if(isEdit&&!created.start_task_id){dialog.value=false;ElMessage.success('配置已保存，重启后生效');await load();return}
  dialog.value=false;await load()
  if(created.start_error)throw new Error(`实例已保存，但自动启动排队失败：${created.start_error}`)
  const deadline=Date.now()+600000
  let task:any
  while(mounted&&Date.now()<deadline){
   task=await api(`/tasks/${created.start_task_id}`)
   if(task.status==='succeeded'||task.status==='failed')break
   await pause()
  }
  await load()
  if(!task||task.status!=='succeeded'){ElMessage.warning('配置已保存，但连接验证未通过，请查看实例状态；原有连接在失败时继续运行');return}
  ElMessage.success('实例已保存并完成真实连接与工具发现')
 }catch(e){formError.value=message(e);ElMessage.error(message(e))}finally{saving.value=false}
}
async function testConnection() {
  testing.value=true; testResult.value=null; testError.value=''
  try {
    const body=buildBody()
    if(!body.name?.trim())throw new Error('请先填写实例名称')
    if(editing.value) body.base_id=editing.value.id
    testResult.value=await post('/instances/test',body)
  } catch(e){testError.value=message(e)} finally{testing.value=false}
}
async function action(item:any,act:string) {
  if(operation.value)return
  if(act==='copy'){await edit(item,true);return}
  if(act==='oauth'){oauthInstance.value=item;return}
  operation.value=item.id
  try {
    const labels:Record<string,string>={stop:'停止',restart:'重启',delete:'移除'}
    if(labels[act]) await ElMessageBox.confirm(`${labels[act]}「${item.name}」？在途调用可能受影响。`,'确认操作',{type:'warning'})
    if(!mounted)return
    if(act==='delete') { await remove(`/instances/${item.id}`); ElMessage.success('实例已移除') }
    else {
      let task=await post(`/instances/${item.id}/${act}`)
      while(mounted&&['pending','queued','running'].includes(task.status)) {
        await pause()
        if(!mounted)return
        task=await api(`/tasks/${task.id}`)
      }
      if(!mounted)return
      if(task.status!=='succeeded') throw new Error(task.detail||'操作未完成，请查看实例状态')
      ElMessage.success('操作完成')
    }
  } catch(e) { if(mounted&&e!=='cancel'&&e!=='close') ElMessage.error(message(e)) }
  finally { if(mounted)await load();operation.value='' }
}
async function showTools(item:any){selected.value=item;effectiveVersion.value=null;tools.value=[];toolsError.value='';toolsBusy.value=true;try{const result=await api(`/instances/${item.id}/tools`);if(selected.value?.id===item.id){tools.value=rows(result);effectiveVersion.value=result.effective_version??null}}catch(e){toolsError.value=message(e)}finally{toolsBusy.value=false}}
const scanDialog=ref(false), refreshedAt=ref('')
let refreshTimer:ReturnType<typeof setTimeout>|undefined
async function autoRefresh(){
 if(!mounted)return
 if(!document.hidden&&!loading.value&&!dialog.value&&!operation.value&&!saving.value){
  try{const data=await api('/instances');if(mounted){items.value=rows(data);error.value='';refreshedAt.value=new Date().toLocaleTimeString('zh-CN')}}catch{}
 }
 if(mounted)refreshTimer=setTimeout(autoRefresh,5000)
}
onMounted(()=>{refreshTimer=setTimeout(autoRefresh,5000)})
async function refresh(){await load();if(!error.value){refreshedAt.value=new Date().toLocaleTimeString('zh-CN');ElMessage.success(`已从服务器刷新，共 ${items.value.length} 个纳管实例`)}}
onUnmounted(()=>{mounted=false;if(refreshTimer)clearTimeout(refreshTimer);if(actionTimer)clearTimeout(actionTimer);finishWait?.()})
</script>
<template>
  <BootSkeleton v-if="loading&&!items.length&&!error" content-only/>
  <template v-else>
  <div class="stats-grid"><div class="stat-card"><span>纳管实例</span><strong>{{items.length}}</strong><small>已保存至平台的 MCP 配置</small></div><div class="stat-card"><span>MCP 已连接</span><strong class="green">{{available}}<i class="live-dot" /></strong><small>MCP 握手与工具发现已通过</small></div><div class="stat-card"><span>接入方式</span><strong class="stat-text">{{remoteCount}} <em>/</em> {{localCount}}</strong><small>远程 HTTP / 本机 stdio</small></div></div>
  <section class="panel"><div class="panel-toolbar"><div><h2>服务实例 <span class="count-badge">{{items.length}}</span></h2><p>配置后自动连接，通过统一网关提供给持有效 Token 的客户端。状态每 5 秒自动刷新。</p></div><div class="toolbar-actions"><el-button :icon="Refresh" :loading="loading" @click="refresh">刷新</el-button><template v-if="auth.admin"><el-button @click="scanDialog=true">扫描导入</el-button><el-button type="primary" :icon="Plus" @click="edit()">添加 MCP</el-button></template></div></div>
  <div class="filter-bar"><el-input v-model="search" placeholder="搜索实例名称" :prefix-icon="Search" clearable @input="page=1" /><el-select v-model="filter" placeholder="全部状态" clearable @change="page=1"><el-option label="MCP 已连接" value="ready"/><el-option label="已停止" value="stopped"/><el-option label="故障" value="fault"/></el-select><el-select v-model="tagFilter" placeholder="全部标签" clearable filterable aria-label="按标签筛选" @change="page=1"><el-option v-for="tag in tagOptions" :key="tag" :label="tag" :value="tag"/></el-select><span class="muted"><template v-if="refreshedAt">更新于 {{refreshedAt}} · </template>{{auth.admin?'管理员可维护配置与运行状态':'成员视图 · 配置只读'}}</span></div>
  <LoadError :error="error" @retry="load()"/><el-table :data="visible" v-loading="loading" row-key="id"><el-table-column label="服务名称" min-width="210"><template #default="{row}"><div class="service-name"><span class="service-icon"><el-icon><Connection/></el-icon></span><div><button class="text-link" @click="showTools(row)">{{row.name}}</button><small>{{row.description||'MCP 服务实例'}}</small><el-tag v-if="row.account_label" size="small" class="instance-label">{{row.account_label}}</el-tag></div></div></template></el-table-column><el-table-column label="传输方式" width="130"><template #default="{row}"><span class="mono transport">{{row.transport==='http'?'HTTP':'STDIO'}}</span></template></el-table-column><el-table-column label="进程状态" width="110"><template #default="{row}"><InstanceHealth :health="row.health" :status="row.status" :transport="row.transport" stage="process" :pid="row.runtime?.pid"/></template></el-table-column><el-table-column label="MCP 测试" width="110"><template #default="{row}"><InstanceHealth :health="row.health" :status="row.status" :transport="row.transport" stage="protocol"/></template></el-table-column><el-table-column label="连接信息" min-width="200" show-overflow-tooltip><template #default="{row}"><span class="muted mono">{{row.url||row.command||'—'}}</span><small v-if="row.last_error" class="error-text">{{row.last_error}}</small><el-button v-if="auth.admin&&row.oauth_required" text type="warning" size="small" @click="oauthInstance=row">需要认证 · 浏览器授权</el-button></template></el-table-column><el-table-column label="操作" :width="auth.admin?260:90" fixed="right"><template #default="{row}"><el-button text size="small" @click="showTools(row)">工具</el-button><template v-if="auth.admin"><el-button text size="small" @click="edit(row)">配置</el-button><el-button text size="small" type="primary" :loading="operation===row.id" :disabled="!!operation" @click="action(row,['available','ready','running'].includes(row.status)?'stop':'start')">{{['available','ready','running'].includes(row.status)?'停止':'启动'}}</el-button><el-dropdown :disabled="!!operation" trigger="click" @command="(cmd:string)=>action(row,cmd)"><el-button text size="small">更多</el-button><template #dropdown><el-dropdown-menu><el-dropdown-item v-if="row.transport==='http'" command="oauth">浏览器授权</el-dropdown-item><el-dropdown-item command="copy">复制并新建</el-dropdown-item><el-dropdown-item command="restart">重启</el-dropdown-item><el-dropdown-item command="delete" divided>移除实例</el-dropdown-item></el-dropdown-menu></template></el-dropdown></template></template></el-table-column><template #empty><el-empty description="暂无 MCP 实例"><el-button v-if="auth.admin" type="primary" @click="edit()">添加第一个 MCP</el-button></el-empty></template></el-table><div class="pagination"><el-pagination v-model:current-page="page" :page-size="10" :total="filtered.length" layout="total, prev, pager, next"/></div></section>
  <div class="hint-strip"><el-icon><Connection/></el-icon><span>{{auth.personal?'个人版默认可管理全部 MCP；客户端持有效 Gateway Token 调用工具。':'实例启动后，有效 Gateway Token 可按所属用户的角色权限调用工具；「安全策略」按客户端来源 IP 控制接入。'}}</span><router-link v-if="auth.admin" to="/rules">配置来源 IP →</router-link></div>
  </template>
  <el-dialog :model-value="!!oauthInstance" title="MCP 浏览器授权" width="680px" destroy-on-close @close="oauthInstance=null;load()"><MCPOAuthAuthorization v-if="oauthInstance" :instance="oauthInstance" @connected="oauthInstance=null;load()"/></el-dialog>
  <el-dialog v-model="dialog" class="mcp-config-dialog" top="4vh" :title="editing?'配置 MCP':copying?'复制 MCP':'添加 MCP'" width="720px" destroy-on-close @closed="resetEditorSecrets">
    <el-alert v-if="copying" title="已复制连接配置及凭据；保存前可修改名称和连接信息，新实例独立运行。角色授权不会自动复制。" type="info" :closable="false"/>
    <el-alert v-if="configLoadError" :title="configLoadError" type="error" :closable="false"/>
    <el-alert v-if="formError" :title="formError" type="error" :closable="false"/>
    <el-radio-group :model-value="configMode" @change="changeConfigMode" style="margin:16px 0"><el-radio-button value="form">表单配置</el-radio-button><el-radio-button value="json">JSON 配置</el-radio-button></el-radio-group>
    <template v-if="configMode==='json'">
      <p class="muted">支持单个服务对象或仅含一个服务的 mcpServers 配置。JSON 包含当前配置的凭据，请勿分享；遗漏 env / headers 时沿用原值，空对象表示清除。凭据加密保存。</p>
      <el-input v-model="jsonText" type="textarea" :rows="16" spellcheck="false" aria-label="MCP JSON 配置" placeholder='{"mcpServers":{"my-server":{"command":"npx","args":["-y","包名"]}}}'/>
      <el-button text @click="discardJSON">放弃 JSON 修改，返回表单</el-button>
      <el-alert v-if="testError" :title="testError" type="error" :closable="false"/>
      <el-alert v-else-if="testResult" :title="`连接成功，发现 ${testResult.count} 个工具`" type="success" :closable="false"/>
    </template>
    <el-form v-else label-position="top" :disabled="configLoading||saving" @submit.prevent="save">
      <div class="form-grid">
        <el-form-item label="实例名称" required><el-input v-model="form.name" placeholder="例如 GitHub 团队服务" maxlength="100"/></el-form-item>
        <el-form-item label="传输方式"><el-select v-model="form.transport"><el-option label="远程 Streamable HTTP" value="http"/><el-option label="本地 stdio" value="stdio"/></el-select></el-form-item>
      </div>
      <el-form-item label="描述"><el-input v-model="form.description" placeholder="说明服务用途"/></el-form-item>
      <el-form-item label="标签"><el-select v-model="form.account_label" filterable allow-create default-first-option clearable placeholder="选择已有标签，或输入新标签后回车" aria-label="实例标签"><el-option v-for="tag in tagOptions" :key="tag" :label="tag" :value="tag"/></el-select><small class="muted">保存后可供其他 MCP 选择，并在实例列表展示和筛选。</small></el-form-item>
      <template v-if="form.transport==='http'">
        <el-form-item label="Server URL" required><el-input v-model="form.url" placeholder="https://example.com/mcp"/></el-form-item>
        <el-form-item label="请求头（Headers，加密保存）">
          <div class="full-width">
            <div v-for="(row,i) in headerRows" :key="i" class="kv-row">
              <el-input v-model="row.key" :aria-label="`请求头名称 ${i+1}`" placeholder="名称，例如 Authorization 或 X-API-Key"/>
              <el-input v-model="row.value" :aria-label="`请求头值 ${i+1}`" type="password" show-password autocomplete="off" placeholder="值，例如 Bearer …"/>
              <el-button :icon="Delete" :aria-label="`删除请求头 ${i+1}`" text @click="headerRows.splice(i,1)"/>
            </div>
            <el-button class="add-config-row" :icon="Plus" @click="headerRows.push({key:'',value:''})">添加请求头</el-button>
            <p class="muted">已有请求头直接编辑，点击删除按钮移除，保存后生效。Bearer 认证填写 Authorization，值为 Bearer 加空格和令牌；无需认证时留空。</p>
          </div>
        </el-form-item>
        <el-collapse v-model="httpAdvanced" class="mcp-optional-config">
          <el-collapse-item name="headers" :title="`高级配置：自动更新请求头（可选）${editing?.header_helper_configured?' · 已配置':''}`">
            <p class="muted">仅用于会过期、需要由脚本自动获取的口令。普通 HTTP MCP 在上方填写固定请求头即可。</p>
            <el-form-item label="获取请求头的本机命令">
              <el-input v-model="form.header_helper" type="password" show-password autocomplete="off" placeholder="例如 python3 /path/to/header-helper.py；留空则关闭"/>
            </el-form-item>
            <p class="muted">每次 HTTP 请求前，在 Toolbox 服务所在电脑执行此命令；脚本输出 JSON 对象，例如 {"X-Tencent-Meeting-Token":"…"}，同名请求头覆盖上方固定值。需在 <router-link to="/system">系统设置</router-link> 启用本机命令运行。已有命令可直接编辑，清空后保存即关闭；请只使用可信脚本。</p>
          </el-collapse-item>
        </el-collapse>
      </template>
      <template v-else>
        <section class="mcp-launch-config" v-loading="configLoading">
        <el-form-item label="启动命令" required><el-input v-model="form.command" placeholder="例如 npx、uvx、python3 或可执行文件路径"/></el-form-item>
        <el-form-item label="参数">
          <div class="full-width">
            <div v-for="(_,i) in argRows" :key="i" class="argument-row">
              <el-input v-model="argRows[i]" :aria-label="`参数 ${i+1}`" :type="privateArgument(argRows[i]||'')?'password':'text'" :show-password="privateArgument(argRows[i]||'')" autocomplete="off" placeholder="一个参数，可包含空格，无需额外加引号"/>
              <el-button :icon="Delete" :aria-label="`删除参数 ${i+1}`" text @click="argRows.splice(i,1)"/>
            </div>
            <el-button class="add-config-row" :icon="Plus" @click="argRows.push('')">添加参数</el-button>
            <p class="muted">按顺序传入命令，每行作为一个完整参数；保留空格和空参数。已保存参数仅管理员可编辑，敏感值默认隐藏，可点击眼睛查看。</p>
          </div>
        </el-form-item>
        <el-form-item label="工作目录"><el-input v-model="form.cwd" placeholder="后端执行环境中的目录"/></el-form-item>
        <el-form-item label="环境变量（加密保存）">
          <div class="full-width">
            <div v-for="(row,i) in envRows" :key="i" class="kv-row"><el-input v-model="row.key" :aria-label="`环境变量名 ${i+1}`" placeholder="键，例如 DATABASE_URL"/><el-input v-model="row.value" :aria-label="`环境变量值 ${i+1}`" type="password" show-password autocomplete="off" placeholder="值"/><el-button :icon="Delete" :aria-label="`删除环境变量 ${i+1}`" text @click="envRows.splice(i,1)"/></div>
            <el-button class="add-config-row" :icon="Plus" @click="envRows.push({key:'',value:''})">添加环境变量</el-button>
            <p class="muted">已有变量直接在此编辑；点击右侧删除按钮移除对应变量，保存后生效。</p>
          </div>
        </el-form-item>
        </section>
        <el-collapse v-if="editing" v-show="pgSupported" v-model="pgExpanded" class="mcp-optional-config"><el-collapse-item name="postgres" title="数据库连接辅助配置（可选）"><PostgresConnections ref="pgEditor" :instance="editing" :draft="pgDraft" @detected="pgSupported=$event" @saved="dialog=false;load()"/></el-collapse-item></el-collapse>
      </template>
      <div class="form-grid">
        <el-form-item label="最大并发"><el-input-number v-model="form.concurrency" :min="1" :max="20"/></el-form-item>
        <el-form-item label="调用超时（秒）"><el-input-number v-model="form.timeout_seconds" :min="1" :max="600"/></el-form-item>
      </div>
      <el-form-item>
        <div class="full-width test-area">
          <el-alert v-if="testError" :title="testError" type="error" :closable="false"/>
          <el-alert v-else-if="testResult" type="success" :closable="false">
            <template #title>连接成功，发现 {{testResult.count}} 个工具</template>
            <div class="mono test-tools">{{(testResult.tools||[]).join('、')}}</div>
          </el-alert>
          <p v-else class="muted">测试会使用当前表单内容真实连接服务并列出工具，不会保存配置、不改变运行状态。</p>
        </div>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="dialog=false">取消</el-button>
      <el-button :disabled="configLoading||!!configLoadError||saving" :loading="testing" @click="testConnection">测试连接</el-button>
      <el-button type="primary" :disabled="configLoading||!!configLoadError||testing" :loading="saving" @click="save">保存配置</el-button>
    </template>
  </el-dialog>
  <el-drawer :model-value="!!selected" @close="selected=null" :title="`${selected?.name||''} · 工具目录`" size="560px"><LoadError :error="toolsError" @retry="showTools(selected)"/><p v-if="selected" class="muted">已保存版本：{{selected.version}} · 运行版本：{{effectiveVersion??'未运行'}}<br/>配置变更需启动或重启后生效。<template v-if="selected.runtime?.started_at"><br/>启动于 {{new Date(selected.runtime.started_at).toLocaleString('zh-CN')}} · 重启 {{selected.runtime.restart_count||0}} 次</template></p><div v-loading="toolsBusy"><el-empty v-if="!tools.length&&!toolsBusy" description="暂无工具，请确认实例已启动"/><div v-for="tool in tools" :key="tool.name" class="tool-card"><strong class="mono">{{tool.name||tool.tool_name}}</strong><p>{{tool.description||'暂无描述'}}</p><el-collapse><el-collapse-item title="参数 Schema"><pre>{{JSON.stringify(tool.inputSchema||tool.input_schema||tool.schema||{},null,2)}}</pre></el-collapse-item></el-collapse></div></div></el-drawer>
  <ScanImport v-model="scanDialog" @imported="load()"/>

</template>

<style>
.mcp-config-dialog{max-height:92vh;display:flex;flex-direction:column}.mcp-config-dialog .el-dialog__body{overflow-y:auto;min-height:0}.mcp-config-dialog .el-dialog__header,.mcp-config-dialog .el-dialog__footer{flex-shrink:0}
.mcp-launch-config{padding:16px;border:1px solid var(--el-border-color);border-radius:12px;margin:8px 0 16px}.argument-row{display:flex;gap:8px;margin-bottom:8px}.argument-row .el-input{flex:1}.add-config-row{width:100%;margin-top:8px}.mcp-optional-config{margin-bottom:16px}
</style>
