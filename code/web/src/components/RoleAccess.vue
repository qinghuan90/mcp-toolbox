<script setup lang="ts">
import { reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, post, patch, message, rows } from '../api'
import { useList } from '../useList'
import LoadError from './LoadError.vue'
defineProps<{roles:any[],users:any[]}>()
const open=defineModel<boolean>({required:true})
const emit=defineEmits<{changed:[]}>()
type Grant={instance_id:string,tools:string[]}
const instances=useList('/instances'),editing=ref<any>(null),formOpen=ref(false),busy=ref(false),error=ref('')
const options=reactive<Record<string,any[]>>({}),toolErrors=reactive<Record<string,string>>({}),toolLoading=reactive<Record<string,boolean>>({})
const form=reactive({name:'',description:'',scope:'all',grants:[] as Grant[],user_ids:[] as string[]})
async function loadTools(id:string){
 if(!id||toolLoading[id])return
 toolLoading[id]=true;toolErrors[id]=''
 try{options[id]=rows(await api(`/instances/${id}/tools`));if(!options[id]?.length)toolErrors[id]='当前服务未运行或未发现工具。可先选择 all，或启动服务后刷新；已保存的指定工具会保留。'}catch(e){toolErrors[id]=message(e)}finally{toolLoading[id]=false}
}
function edit(role?:any){
 editing.value=role||null
 const grants:Grant[]=role?.grants?role.grants.map((g:Grant)=>({instance_id:g.instance_id,tools:[...g.tools]})):(role?.instance_ids||[]).map((id:string)=>({instance_id:id,tools:['*']}))
 Object.assign(form,{name:role?.name||'',description:role?.description||'',scope:role?.scope||(role?'custom':'all'),grants,user_ids:[...(role?.user_ids||[])]});error.value='';formOpen.value=true
 for(const g of grants)void loadTools(g.instance_id)
}
function changeService(grant:Grant){grant.tools=['*'];void loadTools(grant.instance_id)}
function changeTools(grant:Grant,values:string[]){grant.tools=values.includes('*')&&values.length>1?(values[values.length-1]==='*'?['*']:values.filter(x=>x!=='*')):values}
function grantsOf(role:any):Grant[]{return role.grants||(role.instance_ids||[]).map((id:string)=>({instance_id:id,tools:['*']}))}
function toolChoices(grant:Grant){const live=options[grant.instance_id]||[];return [...live,...grant.tools.filter(name=>name!=='*'&&!live.some(t=>t.name===name)).map(name=>({name,description:'已保存的工具；当前目录未发现'}))]}
async function save(){busy.value=true;error.value='';try{
 if(!form.name.trim())throw new Error('请输入角色名称')
 if(form.scope==='custom'&&form.grants.some(g=>!g.instance_id||!g.tools.length))throw new Error('请为每项选择 MCP 服务及对应工具，或选择 all')
 if(new Set(form.grants.map(g=>g.instance_id)).size!==form.grants.length&&form.scope==='custom')throw new Error('同一 MCP 只能配置一次')
 const body={...form,grants:form.scope==='all'?[]:form.grants,version:editing.value?.version}
 if(editing.value)await patch(`/roles/${editing.value.id}`,body);else await post('/roles',body)
 formOpen.value=false;emit('changed');ElMessage.success('角色及 MCP 工具授权已保存')
 }catch(e){error.value=message(e)}finally{busy.value=false}}
async function del(role:any){try{await ElMessageBox.confirm(`删除角色「${role.name}」？成员将失去该角色授予的 MCP 和工具权限，其他角色授权继续有效。`,'删除角色',{type:'warning'});await api(`/roles/${role.id}`,{method:'DELETE',body:JSON.stringify({version:role.version})});emit('changed');ElMessage.success('角色已删除')}catch(e){if(e!=='cancel'&&e!=='close')ElMessage.error(message(e))}}
</script>
<template>
 <el-dialog v-model="open" title="MCP 使用角色管理" width="900px">
  <p class="muted">角色可授权全部 MCP 和工具（all），也可指定 MCP 及其工具。多个角色的权限取并集；管理员拥有全部权限，未分配角色的普通成员无调用权限。</p>
  <div class="toolbar-actions" style="margin-bottom:16px"><el-button type="primary" @click="edit()">创建角色</el-button></div>
  <el-table :data="roles">
   <el-table-column prop="name" label="角色名称" min-width="140"/>
   <el-table-column prop="description" label="说明" min-width="140"/>
   <el-table-column label="MCP 与工具权限" min-width="280"><template #default="{row}">
    <el-tag v-if="row.scope==='all'">all（全部 MCP 和工具）</el-tag>
    <template v-else><div v-for="grant in grantsOf(row)" :key="grant.instance_id" class="role-grant-summary"><strong>{{instances.items.value.find(x=>x.id===grant.instance_id)?.name||grant.instance_id}}</strong><span>{{grant.tools.includes('*')?'all（全部工具）':grant.tools.join('、')}}</span></div><span v-if="!grantsOf(row).length" class="muted">未授权</span></template>
   </template></el-table-column>
   <el-table-column label="成员" min-width="130"><template #default="{row}">{{row.user_ids.map((id:string)=>users.find(x=>x.id===id)?.username||id).join('、')||'无'}}</template></el-table-column>
   <el-table-column label="操作" width="130"><template #default="{row}"><el-button text @click="edit(row)">编辑</el-button><el-button text type="danger" @click="del(row)">删除</el-button></template></el-table-column>
   <template #empty><el-empty description="暂无使用角色，请创建并分配权限与成员"/></template>
  </el-table>
  <p class="muted">撤销授权后，后续工具列表及调用立即校验新权限；已经开始执行的调用不会回滚。MCP 实例新增或重启后，客户端需重新连接以刷新工具目录。</p>
 </el-dialog>
 <el-dialog v-model="formOpen" :title="editing?'编辑 MCP 使用角色':'创建 MCP 使用角色'" width="740px" append-to-body :close-on-click-modal="false" class="role-editor-dialog">
  <LoadError :error="error||instances.error.value" @retry="instances.load()"/>
  <el-form label-position="top" :disabled="busy||instances.loading.value">
   <el-form-item label="角色名称" required><el-input v-model="form.name" maxlength="100" placeholder="例如 数据查询人员"/></el-form-item>
   <el-form-item label="说明"><el-input v-model="form.description" maxlength="2000" placeholder="角色用途"/></el-form-item>
   <el-form-item label="授权范围"><el-radio-group v-model="form.scope"><el-radio-button value="all">all（全部 MCP 和工具）</el-radio-button><el-radio-button value="custom">指定 MCP 和工具</el-radio-button></el-radio-group></el-form-item>
   <p v-if="form.scope==='all'" class="muted">允许全部 MCP 的全部工具，包含后续新增的服务和工具。</p>
   <template v-else>
    <section v-for="(grant,index) in form.grants" :key="index" class="role-tool-grant">
     <div class="panel-toolbar"><strong>MCP 授权 {{index+1}}</strong><el-button text type="danger" :aria-label="`删除 MCP 授权 ${index+1}`" @click="form.grants.splice(index,1)">删除</el-button></div>
     <el-form-item :label="`MCP 服务 ${index+1}`"><el-select v-model="grant.instance_id" filterable placeholder="选择 MCP 服务" @change="changeService(grant)"><el-option v-for="i in instances.items.value" :key="i.id" :value="i.id" :label="i.name" :disabled="form.grants.some((g,j)=>j!==index&&g.instance_id===i.id)"/></el-select></el-form-item>
     <el-form-item :label="`可使用的工具 ${index+1}`"><el-select v-model="grant.tools" multiple filterable :disabled="!grant.instance_id" :loading="toolLoading[grant.instance_id]" placeholder="选择 all 或指定工具" @change="changeTools(grant,$event)"><el-option value="*" label="all（全部工具）"/><el-option v-for="tool in toolChoices(grant)" :key="tool.name" :value="tool.name" :label="tool.name" :title="tool.description"/></el-select></el-form-item>
     <p class="muted">{{toolErrors[grant.instance_id]||'all 包含此 MCP 后续新增的工具；指定工具只授权所选名称。'}}<el-button text :disabled="!grant.instance_id||toolLoading[grant.instance_id]" @click="loadTools(grant.instance_id)">刷新工具列表</el-button></p>
    </section>
    <el-button style="margin-bottom:20px" @click="form.grants.push({instance_id:'',tools:['*']})">添加 MCP 授权</el-button>
    <p v-if="!form.grants.length" class="muted">尚未选择服务，保存后此角色无 MCP 使用权限。</p>
   </template>
   <el-form-item label="分配给用户"><el-select v-model="form.user_ids" multiple filterable placeholder="选择普通成员"><el-option v-for="u in users.filter(x=>x.role!=='admin')" :key="u.id" :value="u.id" :label="u.username+(u.enabled?'':'（已停用）')"/></el-select></el-form-item>
  </el-form>
  <template #footer><el-button @click="formOpen=false">取消</el-button><el-button type="primary" :loading="busy" :disabled="!!instances.error.value||instances.loading.value" @click="save">保存角色</el-button></template>
 </el-dialog>
</template>
<style scoped>
.role-grant-summary{display:flex;flex-direction:column;gap:4px;margin:7px 0;overflow-wrap:anywhere}.role-grant-summary span{font-size:12px;color:#7c8c83}.role-tool-grant{border:1px solid #dfe6e2;border-radius:8px;padding:16px;margin-bottom:16px}.role-tool-grant .panel-toolbar{margin-bottom:8px}.role-tool-grant p{margin-bottom:0}
</style>
<style>
.role-editor-dialog{max-height:92vh;display:flex;flex-direction:column}.role-editor-dialog .el-dialog__body{overflow:auto;min-height:0}.role-editor-dialog .el-dialog__footer{flex-shrink:0}
</style>
