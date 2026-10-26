<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, patch, remove, rows, date, message } from '../api'
import LoadError from './LoadError.vue'
const emit=defineEmits<{directoryChanged:[]}>()
const packages=ref<any[]>([]),settings=ref<any>(null),directory=ref(''),error=ref(''),loading=ref(false),uploading=ref(false),savingDirectory=ref(false),operation=ref(''),file=ref<File|null>(null),fileInput=ref<HTMLInputElement|null>(null)
const form=reactive({version:'',os:'windows',arch:'amd64',min_os:'11',notes:''})
async function load(){loading.value=true;error.value='';try{const [p,s]=await Promise.all([api('/system/packages'),api('/system/package-settings')]);packages.value=rows(p);settings.value=s;directory.value=s.directory}catch(e){error.value=message(e)}finally{loading.value=false}}
async function upload(){if(!file.value||!form.version.trim()){error.value='请选择软件包并填写版本号';return}uploading.value=true;error.value='';try{const body=new FormData();Object.entries(form).forEach(([k,v])=>body.append(k,v));body.append('file',file.value);await api('/system/packages',{method:'POST',body});ElMessage.success('软件包已上传为草稿，发布后才会出现在下载页');file.value=null;if(fileInput.value)fileInput.value.value='';await load()}catch(e){error.value=message(e)}finally{uploading.value=false}}
async function changeState(row:any,status:string){try{await ElMessageBox.confirm(status==='published'?`发布 ${row.version}？该软件包将允许用户从下载页下载。`:`${status==='archived'?'归档':'下架'} ${row.version}？该版本将从下载页移除，文件和版本记录保留。`,'软件包版本管理',{confirmButtonText:'确认',cancelButtonText:'取消'});operation.value=row.id;if(status==='archived')await remove(`/system/packages/${row.id}`);else await patch(`/system/packages/${row.id}`,{status});await load();ElMessage.success('版本状态已更新')}catch(e){if(e!=='cancel'&&e!=='close')error.value=message(e)}finally{operation.value=''}}
async function saveDirectory(){savingDirectory.value=true;error.value='';try{settings.value=await patch('/system/package-settings',{directory:directory.value});directory.value=settings.value.directory;emit('directoryChanged');ElMessage.success('保存目录已更新，并写入服务配置文件')}catch(e){error.value=message(e)}finally{savingDirectory.value=false}}
const platformNames:Record<string,string>={windows:'Windows 11+',darwin:'macOS',linux:'Linux'}
const statusNames:Record<string,string>={draft:'草稿',published:'已发布',archived:'已归档'}
onMounted(load)
</script>
<template>
 <section class="panel">
  <div class="panel-toolbar"><div><h2>软件包管理</h2><p>上传桌面安装包并管理版本，已发布的软件包出现在用户下载页。</p></div><div class="toolbar-actions"><el-button :loading="loading" @click="load">刷新</el-button><router-link to="/download"><el-button>打开下载页</el-button></router-link></div></div>
  <LoadError :error="error" @retry="load"/>
  <el-form label-position="top" :disabled="uploading">
   <div class="package-fields"><el-form-item label="版本号" required><el-input v-model="form.version" placeholder="例如 0.1.9" aria-label="软件包版本号"/></el-form-item><el-form-item label="操作系统"><el-select v-model="form.os" aria-label="软件包操作系统" @change="form.min_os=form.os==='windows'?'11':''"><el-option label="Windows 11 及以上" value="windows"/><el-option label="macOS" value="darwin"/><el-option label="Linux" value="linux"/></el-select></el-form-item><el-form-item label="架构"><el-select v-model="form.arch" aria-label="软件包架构"><el-option label="AMD64 / x86-64" value="amd64"/><el-option label="ARM64" value="arm64"/></el-select></el-form-item><el-form-item label="最低系统版本"><el-input v-model="form.min_os" :disabled="form.os==='windows'" placeholder="例如 13"/></el-form-item></div>
   <el-form-item label="版本说明"><el-input v-model="form.notes" type="textarea" :rows="2" maxlength="4000" placeholder="填写本版本更新内容"/></el-form-item>
   <div class="inline-actions"><input ref="fileInput" type="file" aria-label="选择安装包" accept=".zip,.exe" @change="file=($event.target as HTMLInputElement).files?.[0]||null"/><el-button type="primary" :loading="uploading" :disabled="!file" @click="upload">上传软件包</el-button></div>
   <p class="muted">单包最多 512 MB。服务会校验实际文件格式、目标系统、架构和 SHA256；上传后为草稿，不会自动安装或执行。</p>
  </el-form>
  <el-table :data="packages" v-loading="loading" row-key="id">
   <el-table-column prop="version" label="版本" width="110"/>
   <el-table-column label="系统 / 架构" min-width="160"><template #default="{row}">{{platformNames[row.os]||row.os}} · {{row.arch}}</template></el-table-column>
   <el-table-column label="状态" width="110"><template #default="{row}"><el-tag :type="row.status==='published'?'success':'info'">{{statusNames[row.status]||row.status}}</el-tag></template></el-table-column>
   <el-table-column label="文件" min-width="220" show-overflow-tooltip><template #default="{row}">{{row.filename}}<small>{{(row.size/1024/1024).toFixed(2)}} MB · {{row.integrity==='verified'?'校验通过':'文件待检查或校验失败'}}</small></template></el-table-column>
   <el-table-column label="上传时间" width="170"><template #default="{row}">{{date(row.created_at)}}</template></el-table-column>
   <el-table-column label="操作" width="200"><template #default="{row}"><el-button v-if="row.status!=='published'" text type="primary" :disabled="!!operation||row.integrity!=='verified'" @click="changeState(row,'published')">{{row.status==='archived'?'恢复并发布':'发布'}}</el-button><el-button v-if="row.status==='published'" text :disabled="!!operation" @click="changeState(row,'draft')">下架</el-button><el-button v-if="row.status!=='archived'" text type="danger" :disabled="!!operation" @click="changeState(row,'archived')">归档</el-button></template></el-table-column>
   <template #empty><el-empty description="尚未上传软件包"/></template>
  </el-table>
  <el-divider/>
  <el-form-item label="软件包保存目录"><div class="directory-row"><el-input v-model="directory" aria-label="软件包保存目录" :disabled="!settings?.editable||savingDirectory"/><el-button :loading="savingDirectory" :disabled="!settings?.editable||directory===settings?.directory" @click="saveDirectory">保存目录</el-button></div></el-form-item>
  <p class="muted">目录位于服务所在电脑，默认是程序目录下的 packages。已有软件包时请先在服务器迁移完整目录，再修改配置并重启；页面不会静默搬移或丢弃现有版本。{{settings&&!settings.editable?'当前目录由部署环境变量管理。':''}}</p>
 </section>
</template>
<style scoped>
.package-fields{display:grid;grid-template-columns:repeat(4,minmax(120px,1fr));gap:14px}.directory-row{display:flex;width:100%;gap:12px}.directory-row>.el-input{flex:1}.muted{margin:14px 0}.el-table{margin-top:20px}@media(max-width:900px){.package-fields{grid-template-columns:repeat(2,minmax(120px,1fr))}}
</style>
