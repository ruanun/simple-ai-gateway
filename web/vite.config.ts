import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: { '/admin': 'http://localhost:8080', '/v1': 'http://localhost:8080', '/portal/api': 'http://localhost:8080' },
  },
  build: {
    rollupOptions: {
      output: {
        // 仅 react 核心拆独立 chunk（首屏必需、变更少，缓存价值真实）。
        // ⚠️ 勿把懒加载页面的重依赖（recharts/react-day-picker/@lobehub 等）加进来：
        // manualChunks 命名 chunk 会被注入首屏 modulepreload，反而把按需库提前到登录页下载
        // （实测多预载约 500KB）；不配置时 Vite 会随各懒加载页面自动拆分，无需手动干预。
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('react-router') || id.includes('react-dom') || id.includes('/react/') || id.includes('scheduler')) return 'react'
        },
      },
    },
  },
})
