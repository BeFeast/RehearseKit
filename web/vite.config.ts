/// <reference types="vitest/config" />
import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// The streaming player needs SharedArrayBuffer, which browsers only expose to
// cross-origin isolated pages; the same COOP blocks the Google sign-in popup.
// The Go server (internal/api/static.go) therefore sets the pair on the job
// page /jobs/{id} only, and the dev/preview servers mirror that scope.
const JOB_PAGE = /^\/jobs\/[^/?#]+\/?(?:[?#].*)?$/;

function scopedIsolationHeaders(): Plugin {
  const apply = (server: { middlewares: { use(fn: (req: { url?: string }, res: { setHeader(k: string, v: string): void }, next: () => void) => void): void } }) => {
    server.middlewares.use((req, res, next) => {
      if (JOB_PAGE.test(req.url ?? '')) {
        res.setHeader('Cross-Origin-Opener-Policy', 'same-origin');
        res.setHeader('Cross-Origin-Embedder-Policy', 'credentialless');
      }
      next();
    });
  };
  return { name: 'rk-scoped-isolation-headers', configureServer: apply, configurePreviewServer: apply };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), scopedIsolationHeaders()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
      '/healthz': { target: 'http://127.0.0.1:8080' },
      '/readyz': { target: 'http://127.0.0.1:8080' },
    },
  },
  preview: {
    port: 4173,
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
