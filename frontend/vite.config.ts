import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    // The Go backend embeds this directory into the binary (embed.FS).
    outDir: 'dist',
  },
  server: {
    proxy: {
      // In development, the panel's API and WebSockets run on the Go
      // backend (port 8080); the Vite frontend only serves the SPA.
      // With the exact prefix '/api' the proxy also swallowed /api-docs (the
      // SPA page) and returned an empty 404 in development.
      '^/api/': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
})
