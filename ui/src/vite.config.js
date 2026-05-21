import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  base: '/',
  build: {
    outDir: '../dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom', 'react-router-dom'],
          'vendor-marked': ['marked'],
        },
      },
    },
  },
  server: {
    port: 3002,
    proxy: {
      '/api': 'http://localhost:8080',
      '/catalog/api': 'http://localhost:8080',
      '/catalog/login': 'http://localhost:8080',
      '/catalog/logout': 'http://localhost:8080',
    },
  },
});
