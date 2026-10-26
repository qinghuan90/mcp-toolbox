<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { api, message } from '../api'
import StateTag from './StateTag.vue'
const props=defineProps<{modelValue:boolean}>(),emit=defineEmits(['update:modelValue','submitted'])
const open=computed({get:()=>props.modelValue,set:value=>emit('update:modelValue',value)})
const form=reactive({name:'',runtime:'node',entry:'',args:''}),file=ref<File|null>(null),env=ref<{key:string,value:string}[]>([])
const busy=ref(false),error=ref(''),task=ref<any>(null),uploading=ref(false)
let disposed=false,timer:ReturnType<typeof setTimeout>|undefined,release:(()=>void)|undefined
function choose(event:Event){file.value=(event.target as HTMLInputElement).files?.[0]||null;error.value='';task.value=null}
function pause(){return new Promise<void>(resolve=>{release=resolve;timer=setTimeout(()=>{release=undefined;resolve()},1000)})}
async function install(){
 if(busy.value)return
 error.value='';task.value=null
 try{
  if(!file.value)throw Error('请选择离线 ZIP、TAR 或 TAR.GZ 安装包')
  if(!/\.(zip|tar|tar\.gz|tgz)$/i.test(file.value.name)||file.value.size>128*1024*1024)throw Error('请选择不超过128 MB的 ZIP、TAR、TAR.GZ 或 TGZ 安装包')
  if(!form.name.trim()||!form.entry.trim())throw Error('请填写实例名称与入口文件')
  const environment:Record<string,string>={}
  for(const row of env.value){if(!row.key.trim())throw Error('环境变量名称不能为空');if(Object.hasOwn(environment,row.key.trim()))throw Error('环境变量名称不能重复');environment[row.key.trim()]=row.value}
  const data=new FormData();data.append('file',file.value);data.append('config',JSON.stringify({name:form.name.trim(),runtime:form.runtime,entry:form.entry.trim(),args:form.args.split('\n').filter(x=>x!==''),env:environment}))
  busy.value=true;uploading.value=true
  const queued=await api('/install/offline',{method:'POST',body:data});uploading.value=false;task.value=queued;emit('submitted')
  const deadline=Date.now()+600000
  while(!disposed&&Date.now()<deadline){
   task.value=await api(`/tasks/${queued.id}`)
   if(['succeeded','failed'].includes(task.value.status)){emit('submitted');if(task.value.status==='failed')error.value=task.value.result?.message||task.value.detail;return}
   await pause()
  }
  if(!disposed)throw Error('安装仍在后台执行，请在任务列表查看结果')
 }catch(e){error.value=message(e)}finally{busy.value=false;uploading.value=false}
}
onUnmounted(()=>{disposed=true;if(timer)clearTimeout(timer);release?.()})
watch(()=>props.modelValue,value=>{if(value&&!busy.value){task.value=null;error.value=''}})
</script>
<template>
 <el-dialog v-model="open" class="offline-dialog" top="4vh" title="安装离线 MCP" width="min(680px, 95vw)" :close-on-click-modal="!busy">
  <p class="muted">上传包含 MCP 服务及完整依赖的 ZIP、TAR、TAR.GZ 或 TGZ 包。安装时不下载依赖；创建实例后自动启动并发现工具。</p>
  <el-alert v-if="error" :title="error" type="error" :closable="false"/>
  <el-form label-position="top" :disabled="busy">
   <el-form-item label="离线安装包（ZIP / TAR / TAR.GZ / TGZ，最大128 MB）" required><input aria-label="离线安装包" type="file" accept=".zip,.tar,.tar.gz,.tgz,application/zip,application/x-tar,application/gzip" :disabled="busy" @change="choose"/></el-form-item>
   <el-form-item label="实例名称" required><el-input v-model="form.name" maxlength="120" placeholder="例如 Filesystem 离线服务"/></el-form-item>
   <el-form-item label="运行方式" required><el-radio-group v-model="form.runtime"><el-radio-button value="node">Node.js</el-radio-button><el-radio-button value="python3">Python 3</el-radio-button><el-radio-button value="binary">原生可执行程序</el-radio-button></el-radio-group></el-form-item>
   <el-form-item label="入口文件（相对安装包根目录）" required><el-input v-model="form.entry" placeholder="例如 dist/index.js 或 bin/mcp-server"/><p class="muted">Node.js 包需包含 node_modules；Python 包需包含所需模块；原生程序应匹配服务器操作系统和架构。安装包不支持符号链接、硬链接或特殊文件。</p></el-form-item>
   <el-form-item label="启动参数（可选，每行一个）"><el-input v-model="form.args" type="textarea" :rows="2" placeholder="每行作为一个完整参数，不需要添加引号"/></el-form-item>
   <el-form-item label="环境变量（可选）"><div v-for="(row,index) in env" :key="index" class="inline-actions"><el-input v-model="row.key" placeholder="变量名"/><el-input v-model="row.value" placeholder="变量值" type="password" show-password/><el-button @click="env.splice(index,1)">移除</el-button></div><el-button @click="env.push({key:'',value:''})">添加环境变量</el-button></el-form-item>
  </el-form>
  <p class="muted">确认安装会以 Toolbox 服务账户运行入口程序。请使用可信安装包；凭据会加密保存。</p>
  <div v-if="uploading">正在上传安装包…</div>
  <div v-if="task" class="inline-actions"><StateTag :value="task.status"/><span>{{task.result?.message||'正在解包、安装并验证连接…'}}</span><router-link v-if="task.result?.instance_id" to="/instances">查看实例 →</router-link></div>
  <template #footer><el-button @click="open=false">{{busy?'后台继续':'关闭'}}</el-button><el-button type="primary" :loading="busy" :disabled="busy||task?.status==='succeeded'" @click="install">安装并启动</el-button></template>
 </el-dialog>
</template>
<style>
.offline-dialog{max-height:92vh;display:flex;flex-direction:column}
.offline-dialog .el-dialog__body{overflow-y:auto;min-height:0}
.offline-dialog .el-dialog__header,.offline-dialog .el-dialog__footer{flex-shrink:0}
</style>
