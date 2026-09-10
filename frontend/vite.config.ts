import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// The frontend is embedded into the Go binary at build time and served over the
// admin unix socket under a baseurl prefix fronted by a proxy. The baseurl is NOT
// known at build time and must NOT be baked into the binary — it is resolved at
// runtime from the environment (TERMINAL_ADMIN_BASEURL). So we build with a
// RELATIVE base (./assets/...), and the backend prepends the runtime baseurl to
// asset URLs and injects <base href> when it serves index.html.
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true
  }
})