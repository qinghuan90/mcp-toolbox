import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
export default defineConfig({
  plugins: [vue()],
  server: { proxy: { '/api': process.env.API_TARGET || 'http://127.0.0.1:8090', '/mcp': process.env.API_TARGET || 'http://127.0.0.1:8090' } },
  build: { rollupOptions: { output: { manualChunks(id) { if(id.includes('/node_modules/element-plus/')) return 'element-plus'; if(/\/node_modules\/(vue|@vue|vue-router|pinia)\//.test(id)) return 'vue' } } } },
})
