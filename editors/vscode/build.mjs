// Bundles the extension host entry and the tests with esbuild. `--test` builds
// only the tests (the `pretest` hook); the default run builds dist/extension.js
// and the tests.
import { build } from 'esbuild';
import { rm } from 'node:fs/promises';

const shared = {
  bundle: true,
  platform: 'node',
  target: 'node20',
  format: 'cjs',
  sourcemap: true,
  logLevel: 'info',
};

const testsOnly = process.argv.includes('--test');

if (!testsOnly) {
  await rm('dist', { recursive: true, force: true });
  // `vscode` only exists inside the extension host; `ws` stays external so the
  // tests (and the extension itself) load it from node_modules. Since plan 379
  // `ws` is a runtime dependency: the host opens its own chat socket.
  await build({
    ...shared,
    entryPoints: ['src/extension.ts'],
    outfile: 'dist/extension.js',
    external: ['ws', 'vscode'],
  });
}

await rm('dist-test', { recursive: true, force: true });
await build({
  ...shared,
  entryPoints: ['src/**/*.test.ts'],
  outdir: 'dist-test',
  external: ['ws', 'vscode'],
});