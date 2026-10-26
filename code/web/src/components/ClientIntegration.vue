<script setup lang="ts">
import { ref, reactive, onMounted, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, post, remove, message, date } from '../api'
import { useAuth } from '../auth'
import { isDesktopShell, selectClientDirectory } from '../desktop'
import LoadError from './LoadError.vue'
const emit=defineEmits<{changed:[]}>()
const auth=useAuth(),targets=ref<any[]>([]),integrations=ref<any[]>([]),users=ref<any[]>([]),error=ref(''),busy=ref(false),open=ref(false),preview=ref<any>(null),saving=ref(false),formError=ref(''),replace=ref(false),removing=ref(''),gateway=ref('')
const expanded=ref(false)
const form=reactive({client:'codex',directory:'',server_name:'toolbox',user_id:'',days:30,never_expires:false})
watch(()=>[form.client,form.directory,form.server_name,form.user_id,form.days,form.never_expires],()=>{preview.value=null;replace.value=false})
async function load(){busy.value=true;error.value='';try{const[t,r]=await Promise.all([api('/client-integrations/targets'),api('/client-integrations')]);targets.value=t.items;gateway.value=t.gateway_url;integrations.value=r.items;if(!auth.personal)users.value=(await api('/users')).items||[]}catch(e){error.value=message(e)}finally{busy.value=false}}
function integrationStatus(row:any){if(row.state!=='active')return row.state==='removing'?'撤销待完成':'接入待完成';if(row.token_revoked)return 'Token 已撤销';if(!row.never_expires&&new Date(row.expires_at).getTime()<=Date.now())return 'Token 已到期';return row.last_seen_at?'客户端已访问':'已写入，待客户端加载'}
function selectTarget(id:string){const target=targets.value.find(t=>t.id===id);form.client=id;form.directory=target?.directory||'';preview.value=null;replace.value=false}
function start(id:string){selectTarget(id);form.server_name='toolbox';form.user_id=auth.user?.id||'';form.days=30;form.never_expires=false;formError.value='';open.value=true}
async function browse(){try{const path=await selectClientDirectory();if(path)form.directory=path}catch(e){formError.value=message(e)}}
async function prepare(){formError.value='';saving.value=true;try{preview.value=await post('/client-integrations/preview',{...form});replace.value=false}catch(e){formError.value=message(e)}finally{saving.value=false}}
async function apply(){if(!preview.value)return;saving.value=true;formError.value='';try{await post('/client-integrations',{...form,...preview.value,replace:replace.value});open.value=false;ElMessage.success('接入配置已写入；请在客户端重载 MCP 或重启客户端');await load();emit('changed')}catch(e){formError.value=message(e);await load();emit('changed')}finally{saving.value=false}}
async function resume(row:any){removing.value=row.id;try{await post('/client-integrations',{request_id:row.id});ElMessage.success('接入配置已完成，请重载客户端 MCP');await load();emit('changed')}catch(e){ElMessage.error(message(e))}finally{removing.value=''}}
async function undo(row:any){try{await ElMessageBox.confirm(`移除 ${row.client_name} 中的「${row.server_name}」接入，并撤销对应 Token？如果原来存在同名配置，将恢复原配置；其他 MCP 配置保留。`,'撤销一键接入',{confirmButtonText:'撤销接入',cancelButtonText:'取消',type:'warning'});removing.value=row.id;await remove(`/client-integrations/${row.id}`);ElMessage.success('接入配置已还原，对应 Token 已撤销');await load();emit('changed')}catch(e){if(e!=='cancel'&&e!=='close')ElMessage.error(message(e))}finally{removing.value=''}}
onMounted(load)
</script>
<template>
 <section class="panel integration-panel">
  <button class="integration-summary" type="button" :aria-expanded="expanded" aria-controls="client-integration-options" @click="expanded=!expanded"><span aria-hidden="true">{{expanded?'▾':'▸'}}</span>客户端一键接入<span class="integration-toggle">{{expanded?'收起配置':'展开配置'}}</span></button>
  <div v-show="expanded" id="client-integration-options" class="integration-body">
  <div class="panel-toolbar"><div><p>为 Codex、Qoder、Qoder CN 自动配置 Toolbox Gateway，无需手动复制 Token。</p></div><el-button :loading="busy" @click="load">刷新</el-button></div>
  <LoadError :error="error" @retry="load"/>
  <div class="integration-actions"><el-button type="primary" :disabled="!targets.length" @click="start('codex')">接入客户端</el-button></div>
  <p class="muted">配置写入 Toolbox 服务所在电脑。远程浏览器访问时，不会写入浏览器所在电脑；可在 Gateway 页面复制接入模板。写入前预览，已有文件自动备份。</p>
  <el-table v-if="integrations.length" :data="integrations" row-key="id">
   <el-table-column prop="client_name" label="客户端" min-width="140"/>
   <el-table-column prop="server_name" label="MCP 配置名称" min-width="140"/>
   <el-table-column prop="path" label="配置文件" min-width="250" show-overflow-tooltip/>
   <el-table-column label="写入状态" width="155"><template #default="{row}"><el-tag :type="row.state==='active'?'success':'warning'">{{integrationStatus(row)}}</el-tag></template></el-table-column>
   <el-table-column label="有效期至" width="170"><template #default="{row}">{{row.never_expires?'永久有效':date(row.expires_at)}}</template></el-table-column>
   <el-table-column label="操作" width="180"><template #default="{row}"><el-button v-if="row.state==='pending'" text :loading="removing===row.id" @click="resume(row)">重试接入</el-button><el-button text type="danger" :loading="removing===row.id" @click="undo(row)">撤销接入</el-button></template></el-table-column>
  </el-table>
  <el-collapse v-if="integrations.some(i=>i.backup_path)"><el-collapse-item title="查看原配置备份位置"><p v-for="row in integrations.filter(i=>i.backup_path)" :key="row.id" class="backup-path">{{row.client_name}}：{{row.backup_path}}</p></el-collapse-item></el-collapse>
  </div>
 </section>
 <el-dialog v-model="open" title="客户端一键接入" width="min(760px, 95vw)" destroy-on-close :close-on-click-modal="!saving">
  <el-form label-position="top" :disabled="saving">
   <el-form-item label="客户端 / 配置版本"><el-select v-model="form.client" style="width:100%" @change="selectTarget"><el-option v-for="target in targets" :key="target.id" :label="target.name" :value="target.id"/></el-select></el-form-item>
   <el-form-item label="配置目录"><div class="directory-row"><el-input v-model="form.directory" aria-label="客户端配置目录" placeholder="Toolbox 所在电脑的配置目录绝对路径"/><el-button v-if="isDesktopShell()" @click="browse">选择目录</el-button></div><p class="muted">文件位置：{{targets.find(t=>t.id===form.client)?.relative_file}}；自定义目录应选择配置根目录。</p></el-form-item>
   <el-form-item label="MCP 配置名称"><el-input v-model="form.server_name" aria-label="MCP 配置名称" maxlength="64" placeholder="toolbox"/></el-form-item>
   <el-form-item v-if="!auth.personal" label="Token 所属用户"><el-select v-model="form.user_id" style="width:100%"><el-option v-for="user in users" :key="user.id" :label="user.username" :value="user.id"/></el-select></el-form-item>
   <el-form-item label="Token 有效期"><el-radio-group v-model="form.never_expires"><el-radio-button :value="false">按天设置</el-radio-button><el-radio-button :value="true">永久有效</el-radio-button></el-radio-group></el-form-item>
   <el-form-item v-if="!form.never_expires" label="有效天数"><el-input-number v-model="form.days" :min="1" :max="365" aria-label="Token 有效期"/></el-form-item>
  </el-form>
  <LoadError :error="formError"/>
  <template v-if="preview">
   <el-alert :type="preview.conflict?'warning':'info'" :closable="false" :title="preview.conflict?'已有同名 MCP，请更换名称或确认替换':'确认后自动签发独立 Token 并写入以下配置'"/>
   <p class="backup-path"><strong>实际文件：</strong>{{preview.path}}</p>
   <p class="muted">{{preview.exists?'现有配置会先备份，再保留其他 MCP 和客户端设置。':'文件不存在，确认后自动创建。'}}{{auth.personal?'Token 可调用个人版全部 MCP。':'Token 使用所属用户的角色权限。'}}不会关闭客户端自身的工具调用确认。</p>
   <p v-if="preview.existing_servers.length" class="muted">现有 MCP：{{preview.existing_servers.join('、')}}</p>
   <pre class="integration-preview">{{preview.preview}}</pre>
   <el-checkbox v-if="preview.conflict" v-model="replace">确认替换同名 MCP（撤销接入时可恢复）</el-checkbox>
  </template>
  <template #footer><el-button :disabled="saving" @click="open=false">取消</el-button><el-button :loading="saving" @click="prepare">预览配置</el-button><el-button v-if="preview" type="primary" :loading="saving" :disabled="preview.conflict&&!replace" @click="apply">{{preview.exists?'备份并写入':'确认并写入'}}</el-button></template>
 </el-dialog>
</template>
<style scoped>
.integration-summary{border:0;background:none;color:inherit;padding:0;width:100%;text-align:left;cursor:pointer;font-weight:600;line-height:24px;display:flex;align-items:center;gap:10px}.integration-summary>span:first-child{color:var(--el-text-color-secondary)}.integration-toggle{font-size:12px;font-weight:400;color:var(--el-text-color-secondary);margin-left:auto}.integration-body{margin-top:20px}.integration-actions{display:flex;gap:12px;flex-wrap:wrap}.directory-row{display:flex;width:100%;gap:10px}.directory-row>.el-input{flex:1}.integration-preview{white-space:pre-wrap;overflow-wrap:anywhere;user-select:text;background:#f5f8f6;padding:16px;border-radius:8px;font-size:13px}.backup-path{overflow-wrap:anywhere}.muted{line-height:1.65}
</style>
