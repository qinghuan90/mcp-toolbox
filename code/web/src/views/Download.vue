<script setup lang="ts">
import { useList } from '../useList'
import LoadError from '../components/LoadError.vue'
const list=useList('/desktop/releases')
const names:Record<string,string>={darwin:'macOS',windows:'Windows 11 及以上',linux:'Linux'}
function platformLabel(item:any){return `${names[item.os]||item.os} · ${item.arch==='arm64'?'ARM64（Apple Silicon / ARM）':'AMD64（x86-64）'}`}
function size(value:number){return `${(value/1024/1024).toFixed(2)} MB`}
</script>
<template>
 <main class="download-page">
  <header><router-link to="/instances" class="brand"><div>Toolbox<small>MCP WORKSPACE</small></div></router-link><router-link to="/login">返回 Web 版</router-link></header>
  <section class="download-intro"><span class="eyebrow">TOOLBOX DESKTOP</span><h1>下载本地桌面版</h1><p>下载后独立在你的电脑上运行，使用与你熟悉的 Web 版相同的界面。</p><p>本机数据库、配置和密钥独立保存，免登录管理本机 MCP，无需连接企业服务器。</p></section>
  <LoadError :error="list.error.value" @retry="list.load()"/>
  <section v-loading="list.loading.value" class="download-packages">
   <article v-for="item in list.items.value" :key="item.filename" class="panel download-package">
    <h2>{{platformLabel(item)}}</h2><p>版本 {{item.version}} · {{size(item.size)}}</p><p v-if="item.notes" class="muted">{{item.notes}}</p><p v-if="item.min_os" class="muted">最低系统版本：{{item.os==='windows'?'Windows ':''}}{{item.min_os}}</p>
    <p class="muted">{{item.signature==='notarized'?'已签名并完成公证':item.signature==='signed'?'已签名':'本地构建包，尚未完成开发者签名／公证。系统可能提示无法验证开发者。'}}</p>
    <a class="download-button" :href="item.url" :download="item.filename">下载 {{names[item.os]}} {{item.arch.toUpperCase()}} 版本</a>
    <details><summary>安装包校验信息</summary><p class="muted">{{item.filename}}</p><code>SHA256: {{item.sha256}}</code></details>
   </article>
   <el-empty v-if="!list.loading.value&&!list.error.value&&!list.items.value.length" description="暂未发布可下载的桌面安装包"/>
  </section>
  <section class="download-notes"><h2>运行说明</h2><p>Windows 版支持 Windows 11 及以上，解压后运行 Toolbox.exe；需要 Microsoft Edge WebView2 Runtime。其他系统解压后打开应用。首次运行自动创建本地数据和密钥；个人版免登录，启动后直接以本机超级管理员身份使用。</p><p>本机 MCP 使用的 Node.js、Python 等运行环境需要在电脑上安装，可在“系统设置”查看检测结果。</p><p>macOS 关闭窗口后应用继续在后台运行；请从菜单栏图标或应用菜单选择“退出 Toolbox”停止本地服务。</p></section>
 </main>
</template>
<style scoped>
.download-page{max-width:1080px;margin:auto;padding:36px 28px 64px}.download-page header{display:flex;align-items:center;justify-content:space-between;margin-bottom:70px}.download-page header a{color:#22745d}.download-intro h1{font-size:36px;margin-top:18px}.download-intro p{color:#61766b;line-height:1.8}.download-packages{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:20px;margin:32px 0}.download-package p{line-height:1.7}.download-button{display:inline-block;background:#22745d;color:white;padding:13px 24px;border-radius:8px;margin:8px 0 22px}.download-package details{font-size:12px;color:#708176}.download-package code{display:block;overflow-wrap:anywhere;font-size:11px;margin:12px 0}.download-notes{padding-top:16px;line-height:1.8;color:#61766b}.download-notes h2{color:#243831}
</style>
