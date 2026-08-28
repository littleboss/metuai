import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      // 开发环境中由 Vite 转发请求，浏览器无需处理跨域。
      '/v1': 'http://127.0.0.1:18080',
      '/healthz': 'http://127.0.0.1:18080',
      '/readyz': 'http://127.0.0.1:18080',
      // LiveKit 信令同源代理（与 compose nginx /rtc 一致；目标为宿主机映射端口）。
      '/rtc': {
        target: 'http://127.0.0.1:17880',
        ws: true,
      },
    },
  },
})
