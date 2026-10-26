<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Search, Box } from '@element-plus/icons-vue'
import { useAuth } from '../auth'
import { api, message } from '../api'
import LoadError from '../components/LoadError.vue'
import OfflineInstall from '../components/OfflineInstall.vue'
import OnlineInstall from '../components/OnlineInstall.vue'
const onlineOpen=ref(false),onlinePackage=ref<any>(null)
const offlineOpen=ref(false)
const auth=useAuth(),catalog={items:ref<any[]>([]),loading:ref(false),error:ref('')},q=ref(''),detail=ref<any>(null)
const nextCursor=ref(''), sourceResults=ref<any[]>([])
let catalogSequence=0, activeQuery=''
async function search(more=false){
 if(more && catalog.loading.value)return
 const sequence=++catalogSequence
 if(!more){activeQuery=q.value.trim();nextCursor.value='';catalog.items.value=[];sourceResults.value=[]}
 catalog.loading.value=true;catalog.error.value=''
 try{
 const data=await api('/catalog?q='+encodeURIComponent(activeQuery)+(more&&nextCursor.value?'&cursors='+encodeURIComponent(nextCursor.value):''))
 if(sequence!==catalogSequence)return
 const merged=new Map<string,any>()
 for(const item of [...(more?catalog.items.value:[]),...(data.items||[])]){
 const key=item.name+'@'+item.version,existing=merged.get(key)
 if(existing){existing.catalog_sources=[...new Map([...(existing.catalog_sources||[]),...(item.catalog_sources||[])].map((s:any)=>[s.url,s])).values()]}
 else merged.set(key,item)
 }
 catalog.items.value=[...merged.values()];nextCursor.value=data.next_cursor||'';sourceResults.value=data.sources||[]
 }catch(e){if(sequence===catalogSequence)catalog.error.value=message(e)}finally{if(sequence===catalogSequence)catalog.loading.value=false}
}
function prepare(pkg:any){onlinePackage.value=pkg;onlineOpen.value=true}
onMounted(()=>{search()})
</script>
<template><section class="catalog-hero"><div><div class="eyebrow">MCP REGISTRY</div><h2>发现团队需要的工具</h2><p>查询公开目录，查看接入方式和配置需求。</p></div><el-icon><Box/></el-icon></section><section class="panel"><div class="panel-toolbar"><div class="catalog-search"><el-input v-model="q" :prefix-icon="Search" clearable placeholder="搜索 MCP，例如 github、filesystem" @keyup.enter="search()"/><el-button type="primary" :loading="catalog.loading.value" @click="search()">查询</el-button></div><el-button v-if="auth.admin" type="primary" @click="offlineOpen=true">安装离线 MCP</el-button></div><LoadError :error="catalog.error.value" @retry="search()"/><p class="muted">按 Server 名称搜索，支持英文名称子串，例如 postgres；不检索 Server 内部的 query 等工具名称。已显示 {{catalog.items.value.length}} 项，使用「加载更多」继续查询所有分页。<router-link v-if="auth.admin" to="/system">配置目录仓库</router-link></p><div v-for="source in sourceResults" :key="source.url" style="margin-bottom:8px"><el-alert v-if="source.error" :title="source.name + '：' + source.error" :description="source.url" type="warning" :closable="false"/><span v-else class="muted">{{source.name}} · 本次返回 {{source.count}} 项{{source.next_cursor?'，还有更多结果':''}}</span></div><div class="catalog-grid" v-loading="catalog.loading.value"><button v-for="item in catalog.items.value" :key="item.name+item.version" class="catalog-item" @click="detail=item"><span class="catalog-icon"><el-icon><Box/></el-icon></span><strong>{{item.title||item.name}}</strong><small class="muted">来源：{{(item.catalog_sources||[]).map((s:any)=>s.name).join('、')}}</small><p>{{item.description||'暂无描述'}}</p><div><span class="mono">{{item.version||'版本待选择'}}</span><span>查看详情 →</span></div></button></div><el-button v-if="nextCursor" :loading="catalog.loading.value" @click="search(true)">加载更多 / 重试失败来源</el-button><el-empty v-if="!catalog.items.value.length&&!catalog.loading.value" :description="sourceResults.some(s=>s.error)?'部分仓库查询失败，请查看上方来源状态':'兼容仓库未收录匹配名称，可调整英文名称或添加仓库'"/><div class="panel-note">目录收录不代表安全或兼容性验证。远程 MCP 可在「MCP 管理」中添加，无需安装。</div></section><el-drawer :model-value="!!detail" @close="detail=null" title="目录详情" size="560px"><template v-if="detail"><h2>{{detail.title||detail.name}}</h2><p>{{detail.description}}</p><p v-for="source in detail.catalog_sources||[]" :key="source.url" class="muted">来源：{{source.name}} · {{source.url}}</p><el-divider/><h3>本地包</h3><div v-for="(pkg,index) in detail.packages||[]" :key="index" class="tool-card"><strong>{{pkg.identifier}}</strong><p>{{pkg.registryType}} · {{pkg.version}}</p><el-button v-if="auth.admin&&['npm','pypi'].includes(pkg.registryType)" type="primary" plain @click="prepare(pkg)">安装此版本</el-button></div><p v-if="!detail.packages?.length" class="muted">未提供本地包。</p><h3>远程接入</h3><div v-for="(remote,index) in detail.remotes||[]" :key="index" class="tool-card"><span class="mono">{{remote.url}}</span><p>{{remote.type}}</p></div><p v-if="!detail.remotes?.length" class="muted">未提供远程地址。</p><el-collapse><el-collapse-item title="完整元数据与配置要求"><pre>{{JSON.stringify(detail,null,2)}}</pre></el-collapse-item></el-collapse></template></el-drawer><OnlineInstall v-model="onlineOpen" :pkg="onlinePackage"/><OfflineInstall v-model="offlineOpen"/></template>
