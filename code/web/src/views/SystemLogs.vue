<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { api, date, message } from '../api'
import LoadError from '../components/LoadError.vue'
type Entry = { id:string; time:string; level:string; message:string; attrs?:Record<string,unknown> }
const category=ref('exception'), level=ref(''), search=ref(''), range=ref<[Date,Date]|null>(null)
const items=ref<Entry[]>([]), cursor=ref(''), loading=ref(false), error=ref(''), detail=ref<Entry|null>(null), drawer=ref(false), auto=ref(true), refreshed=ref('')
let timer:number|undefined, controller:AbortController|undefined, revision=0
async function load(more=false,silent=false){
 if(silent&&(loading.value||cursorUsed||drawer.value||document.hidden))return
 const current=++revision
 controller?.abort();controller=new AbortController()
 loading.value=true;error.value=''
 const q=new URLSearchParams({category:category.value,level:level.value,q:search.value,limit:'50'})
 if(range.value){q.set('from',range.value[0].toISOString());q.set('to',range.value[1].toISOString())}
 if(more)q.set('cursor',cursor.value)
 try{const page=await api<{items:Entry[];next_cursor:string}>(`/system/logs?${q}`,{signal:controller.signal});if(current!==revision)return;items.value=more?[...items.value,...page.items]:page.items;cursor.value=page.next_cursor;cursorUsed=more;refreshed.value=new Date().toLocaleTimeString('zh-CN',{hour12:false})}
 catch(e){if(current===revision&&!(e instanceof DOMException&&e.name==='AbortError'))error.value=message(e)}finally{if(current===revision)loading.value=false}
}
let cursorUsed=false
function reset(){if(category.value==='runtime')level.value='';cursorUsed=false;items.value=[];cursor.value='';void load()}
function open(row:Entry){detail.value=row;drawer.value=true}
onMounted(()=>{load();timer=window.setInterval(()=>{if(auto.value)load(false,true)},10000)})
onUnmounted(()=>{window.clearInterval(timer);controller?.abort();revision++})
</script>
<template>
 <section class="panel">
  <div class="panel-toolbar"><div><h2>系统日志</h2><p>保留最近 7 天日志；敏感凭据自动脱敏。异常日志包含警告和错误。</p></div><el-button :loading="loading" @click="load()">刷新</el-button></div>
  <el-tabs v-model="category" @tab-change="reset"><el-tab-pane label="异常日志" name="exception"/><el-tab-pane label="运行日志" name="runtime"/></el-tabs>
  <div class="log-filters">
   <el-input v-model="search" clearable placeholder="搜索日志内容、实例名称或事件" aria-label="搜索系统日志" @keyup.enter="reset" @clear="reset"/>
   <el-select v-if="category==='exception'" v-model="level" clearable placeholder="全部级别" aria-label="日志级别" @change="reset"><el-option label="错误" value="ERROR"/><el-option label="警告" value="WARN"/></el-select>
   <el-date-picker v-model="range" type="datetimerange" start-placeholder="开始时间" end-placeholder="结束时间" @change="reset"/>
   <el-button type="primary" @click="reset">查询</el-button>
  </div>
  <div class="log-status"><el-checkbox v-model="auto">每 10 秒自动刷新</el-checkbox><span class="muted">{{ refreshed?`更新于 ${refreshed}`:'' }} · 已加载 {{items.length}} 条{{cursorUsed?' · 查看历史时暂停自动刷新':''}}</span></div>
  <LoadError :error="error" @retry="load()"/>
  <el-table :data="items" v-loading="loading" @row-click="open" class="log-table" row-key="id">
   <template #empty><el-empty :description="loading?'正在读取日志…':error?'日志读取失败，请重试':category==='exception'?'当前条件下没有异常日志':'当前条件下没有运行日志'" :image-size="70"/></template>
   <el-table-column label="时间" width="175"><template #default="{row}">{{date(row.time)}}</template></el-table-column>
   <el-table-column label="级别" width="85"><template #default="{row}"><el-tag :type="row.level==='ERROR'?'danger':row.level==='WARN'?'warning':'info'">{{row.level==='ERROR'?'错误':row.level==='WARN'?'警告':'信息'}}</el-tag></template></el-table-column>
   <el-table-column prop="message" label="日志内容" min-width="240" show-overflow-tooltip/>
   <el-table-column label="实例 / 事件" min-width="210" show-overflow-tooltip><template #default="{row}">{{row.attrs?.instance_name||row.attrs?.instance_id||row.attrs?.event||'—'}}</template></el-table-column>
   <el-table-column label="操作" width="80"><template #default="{row}"><el-button text @click.stop="open(row)">详情</el-button></template></el-table-column>
  </el-table>
  <div class="log-more"><el-button v-if="cursor" :loading="loading" @click="load(true)">加载更多</el-button><span v-else-if="items.length" class="muted">已显示当前条件下的全部日志</span></div>
 </section>
 <el-drawer v-model="drawer" title="日志详情" size="min(760px, 95vw)"><template v-if="detail"><el-descriptions :column="1" border><el-descriptions-item label="时间">{{date(detail.time)}}</el-descriptions-item><el-descriptions-item label="级别">{{detail.level}}</el-descriptions-item><el-descriptions-item label="内容">{{detail.message}}</el-descriptions-item></el-descriptions><div v-for="(value,key) in detail.attrs" :key="key" class="log-attr"><strong>{{key}}</strong><pre>{{typeof value==='object'?JSON.stringify(value,null,2):value}}</pre></div></template></el-drawer>
</template>
<style scoped>
.log-filters{display:flex;gap:12px;flex-wrap:wrap;align-items:center}.log-filters>.el-input{flex:1;min-width:220px}.log-filters>.el-select{width:140px}.log-status{display:flex;gap:20px;align-items:center;margin:12px 0;flex-wrap:wrap}.log-more{text-align:center;padding-top:20px}.log-attr{margin-top:20px}.log-attr pre{white-space:pre-wrap;overflow-wrap:anywhere;background:var(--bg-soft,#f5f8f6);padding:14px;border-radius:8px;user-select:text}.log-table{cursor:pointer}
</style>
