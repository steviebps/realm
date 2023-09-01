import { resolve } from 'path';
import fs from 'node:fs'
import { defineConfig, splitVendorChunkPlugin } from 'vite'
import react from '@vitejs/plugin-react'
// import basicSsl from '@vitejs/plugin-basic-ssl';

// https://vitejs.dev/config/
export default defineConfig(({ command, mode, ssrBuild }) => {
  return {
    plugins: [
      // basicSsl(),
      react(),
      splitVendorChunkPlugin()
    ],
    base: "/ui",
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
      https: {
        key: fs.readFileSync('/etc/ssl/private/mbp.tail6488a.ts.net.key'),
        cert: fs.readFileSync('/etc/ssl/certs/mbp.tail6488a.ts.net.crt')
      },
      proxy: {
        '/v1': {
          changeOrigin: true,
          target: 'https://mbp.tail6488a.ts.net/',
        }
      },
    }
  }
});
