<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { api, post, message } from '../api'
import StateTag from './StateTag.vue'
const props=defineProps<{modelValue:boolean,pkg?:any}>(),emit=defineEmits(['update:modelValue'])
const open=computed({get:()=>props.modelValue,set:value=>emit('update:modelValue',value)})
const form=reactive({name:'',ecosystem:'npm',package:'',version:'',entry:''})
const args=ref<string[]>([]),env=ref<{key:string,value:string,description?:string}[]>([])
const busy=ref(false),error=ref(''),task=ref<any>(null),entries=ref<string[]>([])
let disposed=false,timer:ReturnType<typeof setTimeout>|undefined,release:(()=>void)|undefined
function pause(){return new Promise<void>(resolve=>{release=resolve;timer=setTimeout(()=>{release=undefined;resolve()},1000)})}
async function install(){
 if(busy.value)return
 error.value='';task.value=null
 try{
  if(!form.name.trim()||!form.package.trim()||!form.version.trim())throw Error('请填写实例名称、包名和固定版本')
  const environment:Record<string,string>={}
  for(const row of env.value){if(!row.key.trim())throw Error('环境变量名不能为空');if(Object.hasOwn(environment,row.key.trim()))throw Error('环境变量名不能重复');environment[row.key.trim()]=row.value}
  busy.value=true
  const queued=await post('/install',{...form,name:form.name.trim(),package:form.package.trim(),version:form.version.trim(),args:args.value,env:environment});task.value=queued
  const deadline=Date.now()+600000
  while(!disposed&&Date.now()<deadline){
   task.value=await api(`/tasks/${queued.id}`)
   if(['succeeded','failed'].includes(task.value.status)){
    entries.value=task.value.result?.entries||[]
    if(task.value.status==='failed')error.value=task.value.result?.message||task.value.detail||'安装失败'
    return
   }
   await pause()
  }
  if(!disposed)throw Error('安装仍在后台执行，请记录任务编号并稍后查看')
 }catch(e){error.value=message(e)}finally{busy.value=false}
}
watch(()=>props.modelValue,value=>{
 if(value&&!busy.value){
  const pkg=props.pkg||{}
  Object.assign(form,{name:(pkg.identifier||'').split('/').pop()||'',ecosystem:pkg.registryType==='pypi'?'pypi':'npm',package:pkg.identifier||'',version:pkg.version||'',entry:''})
  args.value=[];env.value=(pkg.environmentVariables||[]).map((v:any)=>({key:v.name||'',value:v.value||v.default||'',description:v.description||''}))
  error.value='';task.value=null;entries.value=[]
 }
})
onUnmounted(()=>{disposed=true;if(timer)clearTimeout(timer);release?.()})
</script>
<template>
 <el-dialog v-model="open" class="online-install-dialog" top="4vh" title="在线安装 MCP" width="min(700px,95vw)" :close-on-click-modal="!busy">
  <p class="muted">下载固定版本及依赖，创建独立实例，自动启动并验证 MCP 初始化与工具发现。</p>
  <el-alert v-if="error" :title="error" type="error" :closable="false"/>
  <el-form label-position="top" :disabled="busy">
   <el-form-item label="实例名称" required><el-input v-model="form.name" maxlength="120" placeholder="例如 团队数据库"/></el-form-item>
   <el-form-item label="包生态"><el-radio-group v-model="form.ecosystem"><el-radio-button value="npm">npm / Node.js</el-radio-button><el-radio-button value="pypi">PyPI / Python</el-radio-button></el-radio-group></el-form-item>
   <el-form-item label="包名" required><el-input v-model="form.package"/></el-form-item>
   <el-form-item label="固定版本" required><el-input v-model="form.version" placeholder="例如 1.2.3，不使用 latest"/></el-form-item>
   <el-form-item label="可执行入口（单入口自动识别）"><el-select v-if="entries.length>1" v-model="form.entry" placeholder="选择包提供的入口"><el-option v-for="entry in entries" :key="entry" :label="entry" :value="entry"/></el-select><el-input v-else v-model="form.entry" placeholder="留空自动识别；或 module:包名.模块 / object:包名.模块:对象名"/></el-form-item>
   <p v-if="form.ecosystem==='pypi'" class="muted">优先读取 console_scripts；未声明时静态发现可执行模块或 FastMCP 对象。也可填写 module:包名.模块 或 object:包名.模块:对象名，仅允许本包模块，不支持任意 Python 代码或 Shell。</p>
   <el-form-item label="启动参数"><div v-for="(_,index) in args" :key="index" class="inline-actions"><el-input v-model="args[index]" placeholder="一个完整参数，不需额外引号"/><el-button @click="args.splice(index,1)">删除</el-button></div><el-button @click="args.push('')">添加参数</el-button></el-form-item>
   <el-form-item label="环境变量"><div v-for="(row,index) in env" :key="index"><div class="inline-actions"><el-input v-model="row.key" placeholder="变量名，例如 MYSQL_DSN"/><el-input v-model="row.value" placeholder="变量值" type="password" show-password/><el-button @click="env.splice(index,1)">删除</el-button></div><p v-if="row.description" class="muted">{{row.description}}</p></div><el-button @click="env.push({key:'',value:''})">添加环境变量</el-button></el-form-item>
  </el-form>
  <p class="muted">服务端需要 Node.js/npm，或 Python/uv。npm 不执行安装脚本；Python 仅安装 wheel。以 Toolbox 服务账户启动程序，凭据加密保存。</p>
  <div v-if="task"><div class="inline-actions"><StateTag :value="task.status"/><span>{{task.result?.message||(task.status==='queued'?'等待安装':'正在下载安装依赖、解析入口并验证连接…')}}</span></div><p class="muted">任务编号：{{task.id}}</p><router-link v-if="task.result?.instance_id" :to="'/instances?instance='+encodeURIComponent(task.result.instance_id)" @click="open=false">查看实例并配置 / 重试连接 →</router-link></div>
  <template #footer><el-button @click="open=false">{{busy?'后台继续':'关闭'}}</el-button><el-button type="primary" :loading="busy" :disabled="busy||task?.status==='succeeded'||!!task?.result?.instance_id" @click="install">安装并启动</el-button></template>
 </el-dialog>
</template>
<style>
.online-install-dialog{max-height:92vh;display:flex;flex-direction:column}
.online-install-dialog .el-dialog__body{overflow-y:auto;min-height:0}
.online-install-dialog .el-dialog__header,.online-install-dialog .el-dialog__footer{flex-shrink:0}
</style>
