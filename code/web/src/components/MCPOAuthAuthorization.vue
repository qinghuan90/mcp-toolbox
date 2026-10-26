<script setup lang="ts">
import {ref,onMounted,onUnmounted} from 'vue'
import {ElMessageBox} from 'element-plus'
import {api,post,remove,message} from '../api'
import {isDesktopShell,openOAuthBrowser} from '../desktop'
const props=defineProps<{instance:any}>()
const emit=defineEmits<{connected:[]}>()
const status=ref<any>(null),busy=ref(false),error=ref(''),clientID=ref(''),authorization=ref<any>(null)
let timer:ReturnType<typeof setTimeout>|undefined,alive=true
async function refresh(){try{const value=await api(`/instances/${props.instance.id}/oauth`);if(alive)status.value=value}catch(e){if(alive)error.value=message(e)}}
async function poll(){await refresh();if(alive)timer=setTimeout(poll,3000)}
onMounted(poll);onUnmounted(()=>{alive=false;if(timer)clearTimeout(timer)})
async function prepare(){busy.value=true;error.value='';authorization.value=null;try{authorization.value=await post(`/instances/${props.instance.id}/oauth`,{client_id:clientID.value.trim()});await refresh()}catch(e){error.value=message(e)}finally{busy.value=false}}
async function authorize(){error.value='';try{await openOAuthBrowser(authorization.value.authorization_url)}catch(e){error.value=message(e)}}
async function clear(){try{await ElMessageBox.confirm('将清除该实例的本地 OAuth 凭据、取消待完成授权并断开该实例。第三方平台的授权请到对应平台撤销。','清除授权',{type:'warning'});busy.value=true;await remove(`/instances/${props.instance.id}/oauth`);authorization.value=null;await refresh()}catch(e){if(e!=='cancel'&&e!=='close')error.value=message(e)}finally{busy.value=false}}
async function connect(){busy.value=true;error.value='';try{await post(`/instances/${props.instance.id}/restart`);emit('connected')}catch(e){error.value=message(e)}finally{busy.value=false}}
</script>
<template>
 <p>为「{{instance.name}}」授权第三方服务账号。授权由此 MCP 实例共享，Gateway 用户身份仍按平台 Token 记录；需要使用不同第三方账号时，请分别创建实例。</p>
 <el-alert title="仅支持标准 MCP OAuth。普通网页账号、固定 Token 或动态口令请在 MCP 配置中填写请求头；不会自动转换成浏览器授权。" type="info" :closable="false"/>
 <p v-if="status">授权状态：{{({authorized:'凭据已保存',expired:'登录已过期',authorization_required:'服务要求重新授权',not_authorized:'尚未授权'} as Record<string,string>)[status.status]||status.status}}<span v-if="status.pending"> · 等待浏览器完成（10 分钟内有效）</span></p>
 <p v-if="status?.issuer" class="muted">认证服务：{{status.issuer}}</p>
 <el-input v-model="clientID" aria-label="OAuth Client ID" placeholder="Client ID（可选；不支持动态注册时填写预注册的公开客户端 ID）" :disabled="busy"/>
 <el-alert v-if="error" :title="error" type="error" :closable="false" style="margin-top:16px"/>
 <div v-if="authorization" style="margin-top:16px">
  <p>即将访问认证服务：<strong>{{authorization.issuer}}</strong>。请确认服务可信，再点击前往授权并自行确认第三方权限。</p>
  <p class="muted">回调地址：{{authorization.callback}}</p>
  <el-button v-if="isDesktopShell()" type="primary" @click="authorize">前往授权</el-button><a v-else :href="authorization.authorization_url" target="_blank" rel="noopener noreferrer" class="el-button el-button--primary">前往授权</a>
 </div>
 <div style="margin-top:20px;display:flex;gap:8px;flex-wrap:wrap">
  <el-button :loading="busy" @click="prepare">{{status?.status==='authorized'?'重新授权':'准备浏览器授权'}}</el-button>
  <el-button v-if="status?.status==='authorized'" type="primary" :loading="busy" @click="connect">连接服务</el-button>
  <el-button :disabled="busy" @click="refresh">刷新授权状态</el-button>
  <el-button v-if="status?.pending||status?.status!=='not_authorized'" type="danger" plain :disabled="busy" @click="clear">取消并清除授权</el-button>
 </div>
</template>
