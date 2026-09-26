import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

export default defineConfig({
  plugins: [react()],
  base: '/admin/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          antd: ['antd', '@ant-design/icons'],
          react: ['react', 'react-dom', 'react-router-dom'],
        },
      },
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    watch: process.env.DEV_USE_POLLING === 'true'
      ? { usePolling: true, interval: 500 }
      : undefined,
    proxy: {
      '/api/v1/admin': {
        target: process.env.DEV_API_PROXY_TARGET || process.env.VITE_API_BASE || 'http://localhost:8082',
        changeOrigin: true,
      },
      '/api/v1/internal': {
        target: process.env.DEV_API_PROXY_TARGET || process.env.VITE_API_BASE || 'http://localhost:8082',
        changeOrigin: true,
      },
    },
  },
});
