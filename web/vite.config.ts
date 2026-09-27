import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const api = process.env.GRAPHLY_API ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    // The Go server owns /api; proxying keeps cookies same-origin in development.
    proxy: { '/api': { target: api, ws: true, changeOrigin: false } },
  },
})
