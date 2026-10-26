<script setup lang="ts">
import { ref, watch, onUnmounted, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { api, post, message } from '../api'
const props=defineProps<{instance:any,draft?:any}>()
const emit=defineEmits<{saved:[],detected:[supported:boolean]}>()
const supported=ref(false), loading=ref(false), error=ref(''), dialog=ref(false), saving=ref(false), version=ref(0)
const connections=ref<any[]>([])
const modes=['disable','allow','prefer','require','verify-ca','verify-full']
let initialConnection=''
const dirty=computed(()=>supported.value&&connections.value.length>0&&JSON.stringify(connections.value[0])!==initialConnection)
defineExpose({getConnection:()=>dirty.value?{...connections.value[0]}:undefined, getCurrentConnection:()=>supported.value?{...connections.value[0]}:undefined, loading})
let active=true
onUnmounted(()=>{active=false})
watch(()=>props.instance?.id,async id=>{
 supported.value=false;emit('detected',false);error.value='';connections.value=[];dialog.value=false
 if(!id||props.instance?.transport!=='stdio')return
 loading.value=true
 try{const result=await api(`/instances/${id}/postgres`);if(props.instance?.id!==id||!active)return;supported.value=result.supported;emit('detected',!!result.supported);version.value=result.version;connections.value=result.supported?[{...result.connection,password_mode:'keep',password:''}]:[];initialConnection=JSON.stringify(connections.value[0]);if(result.supported&&props.draft)connections.value[0]={...connections.value[0],...props.draft}}
 catch(e){if(props.instance?.id===id)error.value=message(e)}finally{if(props.instance?.id===id)loading.value=false}
},{immediate:true})
function add(){const base=connections.value[0];connections.value.push({...base,name:'',database:'',password_mode:'keep',password:''})}
async function save(){
 saving.value=true;error.value=''
 try{
  const result=await post(`/instances/${props.instance.id}/postgres`,{version:version.value,connections:connections.value.map(c=>({...c,password:c.password_mode==='replace'?c.password:''}))})
  connections.value.forEach(c=>c.password='')
  if(result.start_error)throw new Error(`连接已保存，自动启动未排队：${result.start_error}`)
  const deadline=Date.now()+600000
  let task:any
  while(active&&Date.now()<deadline){task=await api(`/tasks/${result.start_task_id}`);if(task.status==='succeeded'||task.status==='failed')break;await new Promise(resolve=>setTimeout(resolve,1000))}
  if(!active)return
  dialog.value=false;emit('saved')
  if(task?.status!=='succeeded'){ElMessage.warning(`配置已保存，${task?.result?.failed??'部分'} 个数据库连接未生效，请查看实例状态；原有连接在失败时继续运行`);return}
  ElMessage.success(`已保存并验证 ${task.result?.ready??connections.value.length} 个数据库连接`)
 }catch(e){error.value=message(e)}finally{saving.value=false}
}
</script>
<template>
 <div v-if="loading" class="muted">正在识别数据库配置…</div>
 <el-alert v-if="error&&!dialog" :title="error" type="error" :closable="false"/>
 <section v-if="supported&&connections[0]" class="pg-connection">
  <h3>PostgreSQL 数据库连接</h3>
  <p class="muted">直接修改下方库名或连接字段，点击页面底部“保存配置”后自动验证并应用；原密码默认保留。</p>
  <div class="form-grid"><el-form-item label="主机" required><el-input v-model="connections[0].host"/></el-form-item><el-form-item label="端口" required><el-input-number v-model="connections[0].port" :min="1" :max="65535"/></el-form-item></div>
  <div class="form-grid"><el-form-item label="数据库名" required><el-input v-model="connections[0].database" placeholder="要连接的数据库名"/></el-form-item><el-form-item label="数据库用户名" required><el-input v-model="connections[0].username"/></el-form-item></div>
  <el-form-item label="数据库密码"><el-radio-group v-model="connections[0].password_mode"><el-radio-button value="keep">保留原密码</el-radio-button><el-radio-button value="replace">设置新密码</el-radio-button><el-radio-button value="none">不使用密码</el-radio-button></el-radio-group><el-input v-if="connections[0].password_mode==='replace'" v-model="connections[0].password" type="password" show-password autocomplete="new-password"/></el-form-item>
  <el-form-item label="SSL 模式"><el-select v-model="connections[0].sslmode"><el-option label="驱动默认值" value=""/><el-option v-for="mode in modes" :key="mode" :label="mode" :value="mode"/></el-select></el-form-item>
  <el-button plain @click="dialog=true">配置多个数据库连接</el-button>
 </section>
 <el-dialog v-model="dialog" class="postgres-dialog" top="4vh" title="PostgreSQL 数据库连接" width="min(760px, 95vw)" append-to-body :close-on-click-modal="!saving" :close-on-press-escape="!saving" :show-close="!saving" @closed="connections.forEach(c=>c.password='')">
  <el-alert title="首项修改当前实例；新增项复用其程序、参数和环境变量创建独立实例。保存后自动启动，并执行只读 SQL 验证数据库连接。" type="info" :closable="false"/>
  <el-alert v-if="error" :title="error" type="error" :closable="false"/>
  <el-form label-position="top" :disabled="saving">
   <section v-for="(connection,index) in connections" :key="index" class="pg-connection">
    <div class="pg-heading"><strong>{{index===0?'当前连接':`新增连接 ${index}`}}</strong><el-button v-if="index>0" text type="danger" @click="connections.splice(index,1)">删除</el-button></div>
    <el-form-item label="连接名称（同时作为实例名称）" required><el-input v-model="connection.name" maxlength="100" placeholder="例如 PG-业务库只读"/></el-form-item>
    <div class="form-grid"><el-form-item label="主机" required><el-input v-model="connection.host" placeholder="数据库主机或 IP"/></el-form-item><el-form-item label="端口" required><el-input-number v-model="connection.port" :min="1" :max="65535"/></el-form-item></div>
    <div class="form-grid"><el-form-item label="数据库" required><el-input v-model="connection.database"/></el-form-item><el-form-item label="用户名" required><el-input v-model="connection.username" autocomplete="off"/></el-form-item></div>
    <el-form-item label="密码"><el-radio-group v-model="connection.password_mode"><el-radio-button value="keep">沿用原连接密码</el-radio-button><el-radio-button value="replace">设置密码</el-radio-button><el-radio-button value="none">不使用密码</el-radio-button></el-radio-group><el-input v-if="connection.password_mode==='replace'" v-model="connection.password" type="password" show-password autocomplete="new-password" placeholder="连接密码（加密保存，不回显）"/><p v-else-if="connection.password_mode==='keep'" class="muted">{{connection.password_configured?'使用当前实例已保存的密码':'当前实例未保存密码'}}；新增连接可选择设置自己的密码。</p></el-form-item>
    <el-form-item label="SSL 模式"><el-select v-model="connection.sslmode"><el-option label="沿用驱动默认值" value=""/><el-option v-for="mode in modes" :key="mode" :label="mode" :value="mode"/></el-select></el-form-item>
   </section>
   <el-button :disabled="connections.length>=20" @click="add">+ 增加数据库连接</el-button>
  </el-form>
  <template #footer><el-button :disabled="saving" @click="dialog=false">取消</el-button><el-button type="primary" :loading="saving" @click="save">保存并验证连接</el-button></template>
 </el-dialog>
</template>
<style scoped>
.pg-connection{margin:16px 0;padding:16px;border:1px solid var(--el-border-color);border-radius:8px}.pg-heading{display:flex;align-items:center;justify-content:space-between;margin-bottom:12px}
</style>
<style>
.postgres-dialog{max-height:92vh;display:flex;flex-direction:column}.postgres-dialog .el-dialog__body{overflow-y:auto;min-height:0}.postgres-dialog .el-dialog__header,.postgres-dialog .el-dialog__footer{flex-shrink:0}
</style>
