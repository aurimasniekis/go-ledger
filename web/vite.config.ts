import { writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// The production build is written into the Go package that embeds it
// (internal/web/dist). Asset file names carry a content hash, which is what
// lets the server mark them immutable (ADR-0008).
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      // emptyOutDir removes the tracked placeholder that keeps go:embed valid
      // on a checkout without a frontend build; put it back.
      name: 'restore-dist-gitkeep',
      closeBundle() {
        writeFileSync(fileURLToPath(new URL('../internal/web/dist/.gitkeep', import.meta.url)), '')
      },
    },
  ],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // `pnpm dev` proxies the API to a locally running `ledger serve`.
    proxy: { '/api': 'http://127.0.0.1:8080' },
  },
})
