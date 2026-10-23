import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5173,
  },
  preview: {
    host: '127.0.0.1',
    port: 4173,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './tests/setup.ts',
    css: true,
    restoreMocks: true,
    // Default 5 s is too tight for the app-shell tests under full parallel
    // load on HMC; the tests themselves complete, so give them headroom
    // instead of shrinking coverage.
    testTimeout: 15_000,
  },
})
