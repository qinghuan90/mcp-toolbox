<script setup lang="ts">
import { useList } from '../useList'
import { date, remove, message } from '../api'
import { ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useAuth } from '../auth'
import LoadError from '../components/LoadError.vue'
import ClientIntegration from '../components/ClientIntegration.vue'
const list=useList('/clients'),auth=useAuth()
const deleting=ref('')
async function del(row:any){try{await ElMessageBox.confirm(`删除客户端「${row.client_name}」？对应 Token 将立即撤销，历史调用日志保留。此操作不会卸载你电脑上的客户端软件。`,'删除客户端',{type:'warning'});deleting.value=row.id;await remove(`/clients/${row.id}`);await list.load();ElMessage.success('客户端已删除，Token 已撤销')}catch(e){if(e!=='cancel'&&e!=='close')ElMessage.error(message(e))}finally{deleting.value=''}}
function active(row:any){return !row.revoked&&(row.never_expires||new Date(row.expires_at).getTime()>Date.now())}
</script>
<template><ClientIntegration v-if="auth.admin" @changed="list.load()"/><div class="hint-strip">这里显示 {{auth.personal?'本机':auth.admin?'组织':'本人'}} 的 Token 接入记录。最近访问不代表当前在线；配置写入也不代表客户端已经连接。</div><section class="panel"><div class="panel-toolbar"><div><h2>客户端接入</h2><p>{{auth.personal?'本机客户端使用独立 Token 接入，可调用全部 MCP 和工具。':'身份以管理员签发的 Token 归属为准。'}}</p></div><div class="toolbar-actions"><el-button @click="list.load()">刷新</el-button><router-link to="/gateway"><el-button type="primary">接入说明</el-button></router-link></div></div><LoadError :error="list.error.value" @retry="list.load()"/><el-table :data="list.items.value" v-loading="list.loading.value"><el-table-column prop="client_name" label="客户端名称" min-width="180"/><el-table-column v-if="!auth.personal" prop="user_id" label="所属用户 ID" min-width="180" show-overflow-tooltip/><el-table-column label="Token 状态" width="125"><template #default="{row}"><el-tag :type="active(row)?'success':'info'" round>{{row.revoked?'已撤销':active(row)?'有效':'已到期'}}</el-tag></template></el-table-column><el-table-column label="最近访问" min-width="180"><template #default="{row}">{{row.last_seen_at?date(row.last_seen_at):'尚未访问'}}</template></el-table-column><el-table-column label="有效期至" min-width="180"><template #default="{row}">{{row.never_expires?'永久有效':date(row.expires_at)}}</template></el-table-column><el-table-column v-if="auth.admin" label="操作" width="100"><template #default="{row}"><el-button text type="danger" :loading="deleting===row.id" @click="del(row)">删除</el-button></template></el-table-column><template #empty><el-empty description="暂无客户端接入记录"/></template></el-table></section></template>
