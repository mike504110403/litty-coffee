import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    port: 5190,
    // 經 Cloudflare 通道對外測試用（admin.litty-coffee.com → 本機 5190）
    allowedHosts: ['admin.litty-coffee.com'],
    proxy: {
      '/api': 'http://localhost:8790',
      '/media': 'http://localhost:8790',
      '/webhooks': 'http://localhost:8790', // IG webhook 經通道進來
    },
  },
})
