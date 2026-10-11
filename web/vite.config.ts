import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ command }) => ({
  plugins: [react()],
  // Use relative base for production build so it embeds cleanly in Go at /ui/
  base: command === 'build' ? './' : '/',
  server: {
    port: 3000,
    strictPort: true,
    host: true, // Listen on all network interfaces
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        // Ensure SSE (Server-Sent Events) streaming works without buffering
        configure: (proxy) => {
          proxy.on('proxyReq', (_proxyReq, req) => {
            if (req.headers.accept === 'text/event-stream') {
              req.socket.setKeepAlive(true);
            }
          });
        },
      },
      '/healthz': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      '/readyz': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      '/metrics': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        entryFileNames: 'app.js',
        chunkFileNames: 'assets/[name].js',
        assetFileNames: (assetInfo) => {
          if (assetInfo.name && assetInfo.name.endsWith('.css')) {
            return 'app.css';
          }
          return 'assets/[name].[ext]';
        },
      },
    },
  },
}));
