import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: 'internal/web/static/assets',
    emptyOutDir: true,
    rollupOptions: {
      input: 'web/src/main.tsx',
      output: { entryFileNames: 'app.js', assetFileNames: 'app.[ext]' },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['web/src/test-setup.ts'],
    include: ['web/src/**/*.test.tsx'],
  },
})
