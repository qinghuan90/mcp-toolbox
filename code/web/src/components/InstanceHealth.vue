<script setup lang="ts">
import { computed } from 'vue'
const props=defineProps<{health?:any,status:string,transport:string,stage:'process'|'protocol'|'business',pid?:number}>()
const names:Record<string,string>={running:'运行中',observed:'已观测',stopped:'已停止',unknown:'未观测',not_applicable:'远程服务',unverified:'未验证',passed:'已通过',failed:'失败',disconnected:'已断开'}
const state=computed(()=>props.health?.[props.stage]?.state||(props.stage==='process'?(props.transport==='http'?'not_applicable':props.status==='stopped'?'stopped':'unknown'):props.stage==='protocol'&&props.status==='stopped'?'disconnected':'unverified'))
const label=computed(()=>props.stage==='protocol'&&props.status==='quarantined'?'已隔离':props.stage==='protocol'&&props.status==='fault'?'连接故障':names[state.value]||'未验证')
const tagType=computed(()=>props.stage==='protocol'&&['quarantined','fault'].includes(props.status)||state.value==='failed'?'danger':state.value==='passed'?'success':'info')
const detail=computed(()=>{
 const stage=props.health?.[props.stage]
 const parts=[stage?.detail||'暂无本次探测证据']
 if(stage?.checked_at)parts.push('检测时间：'+new Date(stage.checked_at).toLocaleString('zh-CN'))
 if(props.stage==='process'&&props.pid)parts.push('PID '+props.pid)
 if(props.stage==='protocol'){
  if(props.status==='quarantined')parts.push('实例已隔离，禁止调用')
  if(props.status==='fault')parts.push('实例连接故障；本次阶段证据：'+(names[state.value]||'未验证'))
  if(props.health?.tool_count!=null)parts.push('已发现 '+props.health.tool_count+' 个工具')
 }
 if(props.stage==='business'&&props.health?.last_tool_at)parts.push('工具 '+props.health.last_tool+' 最近成功：'+new Date(props.health.last_tool_at).toLocaleString('zh-CN'))
 return parts.join('；')
})
</script>
<template>
 <el-tooltip :content="detail" placement="top">
  <el-tag class="health-stage" size="small" :type="tagType">{{label}}</el-tag>
 </el-tooltip>
</template>
<style scoped>
.health-stage{white-space:nowrap;vertical-align:middle}
</style>
