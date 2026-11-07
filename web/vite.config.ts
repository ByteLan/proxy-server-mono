import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ mode }) => ({
  plugins: [react()],
  server: {
    proxy: {
      '/api': loadEnv(mode, '.', '').VITE_API_TARGET || 'http://127.0.0.1:8080',
      '/sub': loadEnv(mode, '.', '').VITE_API_TARGET || 'http://127.0.0.1:8080',
    },
  },
  build: {
    target: 'es2020',
    chunkSizeWarningLimit: 650,
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [{ name: 'arco', test: /node_modules\/@arco-design/ }],
        },
      },
    },
  },
}));
