import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The SPA is served by the coordinator at the same origin in production, so the
// API base URL is '/'. In `npm run dev` we proxy instead of enabling CORS on the
// Go side.
const API = process.env.ASF_API || 'http://127.0.0.1:8090'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': { target: API, changeOrigin: true },
      '/healthz': { target: API, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
