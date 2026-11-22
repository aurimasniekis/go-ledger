import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    css: false,
    // Above the 5 s asyncUtilTimeout set in src/test/setup.ts, so a slow wait
    // reports its own TestingLibraryElementError rather than a bare test timeout.
    testTimeout: 15000,
  },
})
