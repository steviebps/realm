import { resolve } from 'node:path';
import { defineConfig, UserConfig } from 'vite';
import react, { reactCompilerPreset } from '@vitejs/plugin-react';
import babel from '@rolldown/plugin-babel';
import basicSsl from '@vitejs/plugin-basic-ssl';
import tailwindcss from '@tailwindcss/vite';
import flowbiteReact from 'flowbite-react/plugin/vite';

// https://vitejs.dev/config/
export default defineConfig(({ mode }): UserConfig => {
  return {
    plugins: [basicSsl(), react(), babel({ presets: [reactCompilerPreset()] }), tailwindcss(), flowbiteReact()],
    base: '/ui',
    build: {
      rolldownOptions: {
        input: resolve(import.meta.dirname, 'index.html'),
        output: {
          codeSplitting: {
            groups: [
              { name: 'react', test: /node_modules[\\/](react|react-dom|react-router|scheduler)[\\/]/ },
              { name: 'vendor', test: /node_modules[\\/]/ },
            ],
          },
        },
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
