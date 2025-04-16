import { resolve } from 'path';
import { defineConfig, UserConfig } from 'vite';
import react from '@vitejs/plugin-react';
import basicSsl from '@vitejs/plugin-basic-ssl';

// https://vitejs.dev/config/
export default defineConfig(({ mode }): UserConfig => {
  return {
    plugins: [basicSsl(), react()],
    base: '/ui',
    build: {
      rollupOptions: {
        input: resolve(__dirname, 'index.html'),
      },
      minify: true,
      sourcemap: mode === 'development',
      emptyOutDir: true,
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
