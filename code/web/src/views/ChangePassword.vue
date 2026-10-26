<script setup lang="ts">
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useRouter } from 'vue-router'
import { useAuth } from '../auth'
import { post, message } from '../api'

const router = useRouter()
const auth = useAuth()
const form = reactive({ current_password: '', new_password: '', confirm_password: '' })
const busy = ref(false)
const error = ref('')

async function save() {
  error.value = ''
  if (!form.current_password || !form.new_password || !form.confirm_password) { error.value = '请填写完整信息'; return }
  if (form.new_password !== form.confirm_password) { error.value = '两次输入的新密码不一致'; return }
  const size = new TextEncoder().encode(form.new_password).length
  if (size < 12 || size > 72) { error.value = '新密码须为 12–72 字节'; return }
  busy.value = true
  try {
    await post('/account/password', { current_password: form.current_password, new_password: form.new_password })
    form.current_password = ''
    form.new_password = ''
    form.confirm_password = ''
    auth.user = null
    auth.ready = false
    ElMessage.success('密码已修改，请用新密码登录')
    await router.replace('/login')
  } catch (e) { error.value = message(e) }
  finally { busy.value = false }
}

async function logout() {
  try { await auth.logout(); await router.replace('/login') }
  catch (e) { error.value = message(e) }
}
</script>

<template>
  <div class="login-page">
    <div class="login-story"><div class="brand"><div>Toolbox<small>MCP WORKSPACE</small></div></div><div><div class="eyebrow">SECURE YOUR WORKSPACE</div><h1>先修改初始密码，<br>再开始管理工具。</h1><p>初始管理员密码只用于首次登录。修改后，已有登录会话将失效。</p></div><small>企业级 MCP 管理平台</small></div>
    <div class="login-form"><div class="login-card"><span class="eyebrow">ACCOUNT SECURITY</span><h2>修改初始密码</h2><p>请设置仅你知道的新密码，长度为 12–72 字节。</p><el-alert v-if="error" :title="error" type="error" :closable="false" show-icon /><el-form label-position="top" @submit.prevent="save"><el-form-item label="当前密码"><el-input v-model="form.current_password" type="password" name="current_password" autocomplete="current-password" show-password /></el-form-item><el-form-item label="新密码"><el-input v-model="form.new_password" type="password" name="new_password" autocomplete="new-password" show-password /></el-form-item><el-form-item label="确认新密码"><el-input v-model="form.confirm_password" type="password" name="confirm_password" autocomplete="new-password" show-password /></el-form-item><el-button native-type="submit" type="primary" :loading="busy" class="full-width">保存新密码</el-button></el-form><el-button text @click="logout">退出登录</el-button></div></div>
  </div>
</template>
