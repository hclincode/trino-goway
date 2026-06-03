import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import svgr from 'vite-plugin-svgr';
import { fileURLToPath, URL } from 'node:url';

// The Go gateway serves the bundle under this base path. Keep in sync with the
// React Router `basename` in src/app/router.tsx (see docs/CONVENTIONS.md).
const BASE_PATH = '/trino-gateway/';

// Dev proxy: the app calls `/api/...`; the proxy strips the `/api` prefix and
// forwards to the running Go gateway (matches the original webapp's dev setup).
const PROXY_PATH = process.env.VITE_PROXY_PATH ?? '/api';
const BACKEND_URL = process.env.VITE_BASE_URL ?? 'http://localhost:8080';

export default defineConfig({
  base: BASE_PATH,
  plugins: [react(), svgr()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    // antd and echarts are each irreducibly ~1 MB; we isolate them into their
    // own long-cached vendor chunks rather than try to split them further, so
    // raise the warning threshold above their known size.
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      output: {
        // Split large, stable vendors into their own cacheable chunks so the
        // entry/page bundles stay small.
        manualChunks: {
          react: ['react', 'react-dom', 'react-router-dom'],
          antd: ['antd', '@ant-design/icons'],
          echarts: ['echarts', 'echarts-for-react'],
          query: ['@tanstack/react-query', 'zustand'],
        },
      },
    },
  },
  server: {
    proxy: {
      [PROXY_PATH]: {
        target: BACKEND_URL,
        changeOrigin: true,
        rewrite: (path) => path.replace(new RegExp(`^${PROXY_PATH}`), ''),
      },
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: false,
  },
});
