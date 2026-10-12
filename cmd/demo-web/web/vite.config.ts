import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ command }) => ({
  plugins: [react()],
  base: command === 'build' ? './' : '/',
  server: {
    port: 3001,
    strictPort: true,
    proxy: {
      '/api': 'http://localhost:8090',
      '/healthz': 'http://localhost:8090',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
}));
