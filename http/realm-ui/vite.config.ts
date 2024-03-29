import { resolve } from 'path';
import { defineConfig, splitVendorChunkPlugin } from 'vite';
import react from '@vitejs/plugin-react';
import basicSsl from '@vitejs/plugin-basic-ssl';

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  return {
    plugins: [basicSsl(), react(), splitVendorChunkPlugin()],
    base: '/ui',
    build: {
      manifest: true,
      rollupOptions: {
        input: resolve(__dirname, 'index.html'),
      },
      minify: true,
      sourcemap: mode === 'development',
    },
    server: {
      open: true,
      proxy: {
        '/v1': {
          changeOrigin: true,
          target: 'http://localhost:8080/',
        },
      },
    },
  };
});
