<script setup lang="ts">
import { computed, ref, watch, onUnmounted, nextTick } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, post, rows, message } from '../api'
import LoadError from './LoadError.vue'
import StateTag from './StateTag.vue'
const props=defineProps<{modelValue:boolean}>()
const emit=defineEmits(['update:modelValue','imported'])
const open=computed({get:()=>props.modelValue,set:v=>emit('update:modelValue',v)})
const sources=ref<any[]>([]), discoveries=ref<any[]>([]), sourceResults=ref<any[]>([])
const busy=ref(false), loading=ref(false), error=ref(''), paths=ref(''), scannedAt=ref(''), task=ref<any>(null)
const preview=ref<any>(null), importing=ref(false), confirmed=ref(false)
const discoveryTable=ref<any>(null), selectedRows=ref<any[]>([]), batchBusy=ref(false)
function selectable(row:any){return row.importable&&!row.imported_instance_id}
async function selectEligible(){await nextTick();discoveryTable.value?.clearSelection();for(const row of discoveries.value.filter(selectable))discoveryTable.value?.toggleRowSelection(row,true)}
watch(discoveries,selectEligible)
async function waitStart(id:string){
 const deadline=Date.now()+600000
 while(Date.now()<deadline){
  const task=await api(`/tasks/${id}`)
  if(task.status==='succeeded'||task.status==='failed')return task
  await new Promise(resolve=>setTimeout(resolve,1000))
 }
 throw new Error('连接验证仍在运行，请在实例列表刷新查看状态')
}
async function batchImport(){
 const selected=selectedRows.value.filter(selectable)
 if(!selected.length||batchBusy.value)return
 try{await ElMessageBox.confirm(`将纳管 ${selected.length} 项真实配置，其中 ${selected.filter(x=>x.config?.transport==='stdio').length} 项为本机 stdio。确认后会自动启动并验证连接；本机 stdio 与动态请求头命令会以 Toolbox 服务账户运行。请核对来源及执行权限。`,'批量纳管确认',{type:'warning',confirmButtonText:'确认纳管',cancelButtonText:'取消'})}catch{return}
 batchBusy.value=true;error.value=''
 try{
  const result=await post('/discoveries/batch-import',{items:selected.map(x=>({id:x.id,fingerprint:x.fingerprint}))})
  discoveries.value=rows(await api('/discoveries'));emit('imported')
  if(result.start_error)throw new Error(`已纳管 ${result.count} 项，但自动启动排队失败：${result.start_error}`)
  const started=await waitStart(result.start_task_id)
  emit('imported')
  if(started.status!=='succeeded')throw new Error(`已纳管 ${result.count} 项；${started.result?.failed??result.count} 项连接失败，请查看实例状态`)
  ElMessage.success(`已纳管并连接 ${started.result?.ready??result.count} 项`)
 }catch(e){error.value=message(e)}finally{batchBusy.value=false}
}
let generation=0, timer:ReturnType<typeof setTimeout>|undefined
function stop(){generation++;if(timer)clearTimeout(timer);busy.value=false}
async function initialize(){const g=++generation;loading.value=true;error.value='';try{const [s,d]=await Promise.all([api('/scan/sources'),api('/discoveries')]);if(g!==generation)return;sources.value=rows(s);discoveries.value=rows(d)}catch(e){if(g===generation)error.value=message(e)}finally{if(g===generation)loading.value=false}}
async function poll(id:string,g:number,deadline:number){
  if(g!==generation||!open.value)return
  try{const t=await api(`/tasks/${id}`);if(g!==generation)return;task.value=t
    if(['queued','running'].includes(t.status)){if(Date.now()>deadline)throw new Error('扫描等待超时，请稍后重新打开查看结果');timer=setTimeout(()=>poll(id,g,deadline),1000);return}
    busy.value=false;sourceResults.value=t.result?.sources||[]
    if(t.status!=='succeeded')throw new Error(t.detail||'扫描失败')
    const d=await api('/discoveries');if(g!==generation)return;discoveries.value=rows(d);scannedAt.value=new Date().toLocaleString('zh-CN');ElMessage.success(`扫描完成，发现 ${t.result?.discovered??discoveries.value.length} 项配置`)
  }catch(e){if(g===generation){busy.value=false;error.value=message(e)}}
}
async function scan(){if(busy.value)return;const g=++generation;busy.value=true;error.value='';sourceResults.value=[]
 try{const t=await post('/scan',{paths:paths.value.split('\n').map(x=>x.trim()).filter(Boolean)});if(g!==generation)return;task.value=t;await poll(t.id,g,Date.now()+120000)}catch(e){if(g===generation){busy.value=false;error.value=message(e)}}}
function inspect(row:any){preview.value=row;confirmed.value=false;error.value=''}
async function importSelected(){if(!preview.value||!confirmed.value||importing.value)return;importing.value=true;error.value=''
 try{
  const result=await post(`/discoveries/${preview.value.id}/import`,{fingerprint:preview.value.fingerprint})
  preview.value=null;discoveries.value=rows(await api('/discoveries'));emit('imported')
  if(result.start_error)throw new Error(`已纳管，但自动启动排队失败：${result.start_error}`)
  const started=await waitStart(result.start_task_id)
  emit('imported')
  if(started.status!=='succeeded')throw new Error('已纳管，但连接失败，请在实例列表查看原因并重试')
  ElMessage.success('已纳管并完成真实连接与工具发现')
 }catch(e){error.value=message(e)}finally{importing.value=false}}
watch(()=>props.modelValue,v=>{if(v)initialize();else{stop();preview.value=null}})
onUnmounted(stop)
</script>
<template>
 <el-dialog v-model="open" title="发现本机 MCP 配置" width="min(920px, 95vw)">
  <p class="muted">读取平台后端所在电脑的已知客户端配置。扫描不会运行命令；确认纳管后自动启动并验证连接。</p>
  <LoadError :error="error" @retry="initialize"/>
  <div v-loading="loading">
   <el-table :data="sources"><el-table-column prop="name" label="配置来源" width="140"/><el-table-column prop="path" label="实际路径" show-overflow-tooltip/><el-table-column label="读取状态" width="180"><template #default="{row}"><span :class="row.available?'':'muted'">{{row.available?'可扫描':row.reason||'未发现'}}</span></template></el-table-column></el-table>
   <el-collapse><el-collapse-item title="添加配置文件或目录（可选）"><p class="muted">始终扫描上方已知来源与已保存目录；每行可临时添加一个 JSON / TOML 文件或 MCP 配置目录。本机服务支持当前用户目录。常用目录可在 <router-link to="/system">系统设置 → 扫描目录</router-link> 保存。</p><el-input v-model="paths" type="textarea" :rows="2" placeholder="每行一个配置文件或目录的绝对路径"/></el-collapse-item></el-collapse>
   <div class="inline-actions"><el-button type="primary" :loading="busy" @click="scan">扫描本机配置</el-button><StateTag v-if="task" :value="task.status"/><span class="muted" v-if="scannedAt">扫描于 {{scannedAt}}</span></div>
   <div v-for="(result,index) in sourceResults" :key="index" class="muted">{{result.path}}：{{result.error||`${result.discovered??0} 项配置`}}</div>
   <div class="panel-toolbar"><h3>发现的配置 <span class="count-badge">{{discoveries.length}}</span></h3><el-button type="primary" :disabled="!selectedRows.length||busy" :loading="batchBusy" @click="batchImport">批量纳管（{{selectedRows.length}}）</el-button></div>
   <p class="muted">默认全选可纳管且尚未纳管的配置；需要补充配置的条目可点“查看配置”处理。</p>
   <el-table ref="discoveryTable" :data="discoveries" max-height="360" @selection-change="(value:any[])=>selectedRows=value"><el-table-column type="selection" width="50" :selectable="selectable"/><el-table-column prop="name" label="名称" min-width="160"/><el-table-column prop="source" label="来源" show-overflow-tooltip/><el-table-column label="状态" width="130"><template #default="{row}">{{row.imported_instance_id?'已纳管':row.importable?'待纳管':'需要补充配置'}}</template></el-table-column><el-table-column label="操作" width="110"><template #default="{row}"><el-button text type="primary" @click="inspect(row)">查看配置</el-button></template></el-table-column><template #empty><el-empty description="尚未发现配置，请点击扫描本机配置" :image-size="60"/></template></el-table>
  </div>
 </el-dialog>
 <el-dialog :model-value="!!preview" @close="preview=null" title="确认纳管配置" width="min(650px, 95vw)">
  <template v-if="preview"><h3>{{preview.name}}</h3><p class="muted">来源：{{preview.source}}</p>
   <el-descriptions :column="1" border>
    <el-descriptions-item label="连接方式">{{preview.config?.transport}}</el-descriptions-item>
    <el-descriptions-item label="地址 / 命令">{{preview.config?.url||preview.preview?.command||preview.config?.command}}</el-descriptions-item>
    <el-descriptions-item v-if="preview.config?.transport==='stdio'" label="启动参数">{{JSON.stringify(preview.preview?.args||preview.config?.args||[])}}</el-descriptions-item>
    <el-descriptions-item v-if="preview.config?.transport==='stdio'" label="工作目录">{{preview.preview?.cwd||preview.config?.cwd||'服务进程工作目录'}}</el-descriptions-item>
    <el-descriptions-item v-if="preview.config?.transport==='stdio'" label="目录状态">{{preview.preview?.directory?.exists?(preview.preview?.directory?.is_directory?'目录存在':'不是目录'):'目录不存在'}} · {{preview.preview?.directory?.mode||'权限未知'}}<span v-if="preview.preview?.directory?.symlink"> · 符号链接</span></el-descriptions-item>
    <el-descriptions-item label="凭据字段">{{[...(preview.preview?.env_keys||preview.config?.env_keys||[]),...(preview.preview?.header_keys||preview.config?.header_keys||[])].join('、')||'无'}}</el-descriptions-item>
    <el-descriptions-item v-if="preview.preview?.header_helper_configured" label="动态凭据">已配置请求头命令；启动 HTTP 实例时会运行，请确认命令来源可信</el-descriptions-item>
   </el-descriptions>
   <p v-if="preview.preview?.notice" class="muted">{{preview.preview.notice}}</p>
   <el-alert v-for="(warning,index) in preview.warnings||[]" :key="index" :title="warning" type="warning" :closable="false"/>
   <p class="muted">stdio 启动命令及 HTTP 动态请求头助手会以 Toolbox 服务账户权限在本机运行。请核对命令、参数和来源；掩码项仍使用来源文件的实际值。纳管会加密保存凭据且不修改来源文件；确认后自动启动并验证工具。有效 Gateway Token 按用户角色权限调用已启动 MCP，来源 IP 由安全策略控制。</p>
   <el-checkbox v-if="preview.importable&&!preview.imported_instance_id" v-model="confirmed">已核对执行目标及访问权限，确认纳管此配置和凭据</el-checkbox>
   <LoadError :error="error"/>
  </template>
  <template #footer><el-button @click="preview=null">关闭</el-button><el-button type="primary" :disabled="!confirmed||!preview?.importable||!!preview?.imported_instance_id" :loading="importing" @click="importSelected">{{preview?.imported_instance_id?'已纳管':'确认纳管'}}</el-button></template>
 </el-dialog>
</template>
