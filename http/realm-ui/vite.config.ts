import { resolve } from 'path';
import { defineConfig, UserConfig } from 'vite';
import react from '@vitejs/plugin-react';
import basicSsl from '@vitejs/plugin-basic-ssl';
import tailwindcss from "@tailwindcss/vite";
import flowbiteReact from "flowbite-react/plugin/vite";

const ReactCompilerConfig = {
  target: '18' // '17' | '18' | '19'
};

// https://vitejs.dev/config/
export default defineConfig(({ mode }): UserConfig => {
  return {
    plugins: [basicSsl(), react({
      babel: {
        plugins: [
          ["babel-plugin-react-compiler", ReactCompilerConfig],
        ],
      },
    }), tailwindcss(), flowbiteReact()],
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