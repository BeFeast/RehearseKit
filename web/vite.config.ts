/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// The streaming player needs SharedArrayBuffer, which browsers only expose to
// cross-origin isolated pages. The Go server sets these on /jobs/*; the dev
// server sets them everywhere so `bun run dev` behaves the same.
const isolationHeaders = {
  'Cross-Origin-Opener-Policy': 'same-origin',
  'Cross-Origin-Embedder-Policy': 'credentialless',
};

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    headers: isolationHeaders,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
      '/healthz': { target: 'http://127.0.0.1:8080' },
      '/readyz': { target: 'http://127.0.0.1:8080' },
    },
  },
  preview: {
    port: 4173,
    headers: isolationHeaders,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
      '/healthz': { target: 'http://127.0.0.1:8080' },
      '/readyz': { target: 'http://127.0.0.1:8080' },
    },
  },
  build: {
    outDir: 'dist',
    // dist/ holds a committed placeholder (placeholder.html + .gitkeep) that the
    // Go embed falls back to; `bun run build` clears dist/assets itself.
    emptyOutDir: false,
    sourcemap: false,
    target: 'es2022',
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    css: false,
  },
});
