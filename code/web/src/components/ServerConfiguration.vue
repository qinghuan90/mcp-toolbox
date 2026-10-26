<script setup lang="ts">
import { ref,onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { api,patch,message } from '../api'
import LoadError from './LoadError.vue'
const config=ref<any>(null),values=ref<any>({}),cidrs=ref(''),error=ref(''),busy=ref(false)
function assign(result:any){config.value=result;values.value={...result.values};cidrs.value=(result.values.access_cidrs||[]).join('\n')}
async function load(){error.value='';try{assign(await api('/system/server-config'))}catch(e){error.value=message(e)}}
async function save(){busy.value=true;error.value='';try{assign(await patch('/system/server-config',{...values.value,access_cidrs:cidrs.value.split(/\r?\n/).map(x=>x.trim()).filter(Boolean)}));ElMessage.success('配置文件已保存，重启企业服务后生效')}catch(e){error.value=message(e)}finally{busy.value=false}}
function managed(field:string){return !!config.value?.environment_overrides?.[field]}
onMounted(load)
</script>
<template><section class="panel"><div class="panel-toolbar"><div><h2>企业服务配置</h2><p>配置文件：{{config?.path||'读取中…'}}。相对路径按配置文件所在目录解析。</p></div><el-button @click="load">刷新状态</el-button></div><LoadError :error="error" @retry="load"/>
 <el-form v-if="config" label-position="top" :disabled="busy"><div class="server-fields"><el-form-item label="监听地址"><el-input v-model="values.listen_host" :disabled="managed('listen_host')"/></el-form-item><el-form-item label="监听端口"><el-input-number v-model="values.listen_port" :min="1024" :max="65535" :disabled="managed('listen_port')"/></el-form-item><el-form-item label="数据库文件"><el-input v-model="values.database_path" :disabled="managed('database_path')"/></el-form-item><el-form-item label="软件包目录"><el-input v-model="values.package_directory" disabled/><small>在“软件包管理”中维护保存目录。</small></el-form-item><el-form-item label="外部访问地址"><el-input v-model="values.public_url" :disabled="managed('public_url')" placeholder="例如 https://tools.example.com"/></el-form-item><el-form-item label="允许访问的来源 CIDR（每行一个）"><el-input v-model="cidrs" type="textarea" :rows="2" :disabled="managed('access_cidrs')" placeholder="例如 192.168.1.0/24"/></el-form-item><el-form-item label="TLS 证书文件"><el-input v-model="values.tls_cert_file" :disabled="managed('tls_cert_file')"/></el-form-item><el-form-item label="TLS 私钥文件"><el-input v-model="values.tls_key_file" :disabled="managed('tls_key_file')"/></el-form-item></div><p class="muted">保存此处配置后需要重启服务；如需立即切换端口，请使用下方“服务端口”。变更数据库文件会使用新的数据库，请先准备迁移。环境变量和启动参数优先，并会锁定对应字段。监听局域网地址需配置 TLS、HTTPS 访问地址和来源 CIDR。</p><el-alert v-if="config.restart_required" title="存在待生效配置，请重启企业服务" type="info" :closable="false"/><el-button type="primary" :loading="busy" @click="save">保存配置文件</el-button></el-form>
</section></template>
<style scoped>.server-fields{display:grid;grid-template-columns:repeat(2,minmax(180px,1fr));gap:0 20px}.server-fields .el-input-number{width:100%}.muted{line-height:1.7}@media(max-width:760px){.server-fields{grid-template-columns:1fr}}</style>
