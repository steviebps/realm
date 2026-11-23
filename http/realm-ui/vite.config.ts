import { readdirSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { defineConfig, Plugin, UserConfig } from 'vite';
import react, { reactCompilerPreset } from '@vitejs/plugin-react';
import babel from '@rolldown/plugin-babel';
import basicSsl from '@vitejs/plugin-basic-ssl';
import tailwindcss from '@tailwindcss/vite';
import flowbiteReact from 'flowbite-react/plugin/vite';

// The licenses of the bundled packages, written by Vite (build.license) and
// completed by assetLicenses. It ships with the UI and in the release
// notices (scripts/notices).
const licenseFile = 'licenses.md';

// assetLicenses adds to Vite's license file the packages behind emitted
// assets, such as the fonts index.css imports: Vite lists only the packages
// in the JavaScript chunks. Entries follow Vite's format.
function assetLicenses(): Plugin {
  let root = '';
  return {
    name: 'realm:asset-licenses',
    apply: 'build',
    configResolved(config) {
      root = config.root;
    },
    generateBundle: {
      // after Vite has written the file
      order: 'post',
      handler(_, bundle) {
        const file = bundle[licenseFile];
        if (file?.type !== 'asset') {
          return;
        }
        let text = String(file.source);
        const seen = new Set<string>();
        for (const asset of Object.values(bundle)) {
          if (asset.type !== 'asset') {
            continue;
          }
          for (const source of asset.originalFileNames) {
            const at = source.lastIndexOf('node_modules/');
            if (at < 0) {
              continue;
            }
            const [scope, name] = source.slice(at + 'node_modules/'.length).split('/');
            const dir = resolve(
              root,
              source.slice(0, at),
              'node_modules',
              scope.startsWith('@') ? `${scope}/${name}` : scope
            );
            if (seen.has(dir)) {
              continue;
            }
            seen.add(dir);
            const pkg = JSON.parse(readFileSync(resolve(dir, 'package.json'), 'utf8'));
            const heading = `\n## ${pkg.name} - ${pkg.version}${pkg.license ? ` (${pkg.license})` : ''}\n`;
            if (text.includes(heading)) {
              continue;
            }
            text += heading;
            const license = readdirSync(dir).find((f) => /^(licen[cs]e|copying)/i.test(f));
            if (license) {
              text += `\n${readFileSync(resolve(dir, license), 'utf8').trim()}\n`;
            }
          }
        }
        file.source = text;
      },
    },
  };
}

// https://vitejs.dev/config/
export default defineConfig(({ mode }): UserConfig => {
  return {
    plugins: [
      basicSsl(),
      react(),
      babel({ presets: [reactCompilerPreset()] }),
      tailwindcss(),
      flowbiteReact(),
      assetLicenses(),
    ],
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
      license: { fileName: licenseFile },
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
