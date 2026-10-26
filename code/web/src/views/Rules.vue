<script setup lang="ts">
import { reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useAuth } from '../auth'
import { useList } from '../useList'
import { post, remove, message } from '../api'
import LoadError from '../components/LoadError.vue'

const auth = useAuth()
const list = useList('/rules')
const open = ref(false)
const busy = ref(false)
const error = ref('')
const form = reactive({ cidr: '', effect: 'allow' })

function create() {
  form.cidr = ''
  form.effect = 'allow'
  error.value = ''
  open.value = true
}

async function save() {
  busy.value = true
  error.value = ''
  try {
    if (!form.cidr.trim()) throw new Error('请输入来源 IP 或 CIDR 网段')
    await post('/rules', { cidr: form.cidr.trim(), effect: form.effect })
    open.value = false
    await list.load()
    ElMessage.success('IP 规则已保存')
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

async function del(row: any) {
  try {
    await ElMessageBox.confirm(`删除「${row.cidr}」的来源 IP 规则？`, '删除规则', { type: 'warning' })
    await remove(`/rules/${row.id}`)
    await list.load()
    ElMessage.success('IP 规则已删除')
  } catch (e) {
    if (e !== 'cancel' && e !== 'close') ElMessage.error(message(e))
  }
}
</script>

<template>
  <div class="policy-banner">
    <div class="policy-symbol">IP</div>
    <div>
      <h3>按客户端来源 IP 控制 Gateway 调用</h3>
      <p>{{auth.personal?'个人版默认可使用全部 MCP；本页仅按来源 IP 限制 Gateway 接入。':'角色决定用户可使用的 MCP 服务；本页按来源 IP 限制接入，不逐个授权服务内部工具。'}}</p>
    </div>
    <span class="version-pill">全局 IP 规则</span>
  </div>
  <section class="panel">
    <div class="panel-toolbar">
      <div>
        <h2>来源 IP 安全策略</h2>
        <p>没有允许规则时，持有效 Token 的客户端可连接；添加允许规则后，仅匹配允许网段的来源可连接。禁止规则始终优先。</p>
        <p>使用 TCP 连接的实际来源 IP，不采信客户端传入的 X-Forwarded-For。若部署在反向代理后面，须在网络层传递真实来源地址。</p>
      </div>
      <div class="toolbar-actions">
        <el-button @click="list.load()">刷新</el-button>
        <el-button v-if="auth.admin" type="primary" @click="create">添加 IP 规则</el-button>
      </div>
    </div>
    <LoadError :error="list.error.value" @retry="list.load()" />
    <el-table :data="list.items.value" v-loading="list.loading.value">
      <el-table-column prop="cidr" label="来源 IP / CIDR" min-width="240" />
      <el-table-column label="规则" width="130">
        <template #default="{ row }"><el-tag :type="row.effect === 'allow' ? 'success' : 'danger'" round>{{ row.effect === 'allow' ? '允许' : '禁止' }}</el-tag></template>
      </el-table-column>
      <el-table-column v-if="auth.admin" label="操作" width="100">
        <template #default="{ row }"><el-button text type="danger" @click="del(row)">删除</el-button></template>
      </el-table-column>
      <template #empty><el-empty description="暂无 IP 规则；有效 Token 可调用已启动的工具" /></template>
    </el-table>
  </section>
  <el-dialog v-model="open" title="添加来源 IP 规则" width="520px">
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <el-form label-position="top">
      <el-form-item label="来源 IP 或 CIDR 网段" required><el-input v-model="form.cidr" placeholder="例如 192.168.1.20 或 192.168.1.0/24" /></el-form-item>
      <el-form-item label="规则类型"><el-radio-group v-model="form.effect"><el-radio value="allow">允许</el-radio><el-radio value="deny">禁止</el-radio></el-radio-group></el-form-item>
      <el-alert title="允许规则会将 Gateway 限定到匹配网段；如需本机客户端继续接入，请同时允许 127.0.0.1 或 ::1。" type="warning" :closable="false" />
    </el-form>
    <template #footer><el-button @click="open = false">取消</el-button><el-button type="primary" :loading="busy" @click="save">保存规则</el-button></template>
  </el-dialog>
</template>
