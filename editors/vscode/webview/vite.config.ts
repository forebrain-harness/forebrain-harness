// The chat webview's own build. The aliases mirror the web app's (vite
// resolved them for the components this webview reuses), `dedupe` keeps one
// Vue on both sides of the workspace boundary, and the version define reads
// the same VERSION file — one number everywhere.
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { readFileSync } from 'fs'
import { dirname, resolve } from 'path'
import { fileURLToPath } from 'url'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '../../..')
const forebrainVersion = readFileSync(resolve(repoRoot, 'VERSION'), 'utf-8').trim()

export default defineConfig({
  plugins: [vue()],
  // Component tests mount real components, so they need a DOM. The markdown
  // renderer's stylesheet needs the same inlining the web app's tests use:
  // externalized deps never pass through the PostCSS pipeline. The panel's
  // copy is the web's copy: the tests read in Chinese.
  test: {
    environment: 'jsdom',
    server: { deps: { inline: ['vue-stream-markdown'] } },
    setupFiles: ['./src/test-setup.ts'],
  },
  define: {
    __FOREBRAIN_VERSION__: JSON.stringify(forebrainVersion),
  },
  resolve: {
    alias: {
      '@': resolve(repoRoot, 'frontend/src'),
      '@repo/elements': resolve(repoRoot, 'ai-elements-vue/packages/elements/src'),
      '@repo/shadcn-vue': resolve(repoRoot, 'ai-elements-vue/packages/shadcn-vue'),
      // The markdown renderer's probe for mermaid must fail, not resolve to an
      // empty stand-in — the same substitute the web app builds with.
      mermaid: resolve(repoRoot, 'frontend/src/lib/mermaid-unavailable.ts'),
    },
    dedupe: ['nanoid', 'vue'],
  },
  build: {
    outDir: 'dist',
    // Two apps, one bundle graph: the chat workbench and the Sessions view.
    // Vite only takes index.html by default, so the Sessions entry is named.
    rollupOptions: {
      input: {
        index: resolve(here, 'index.html'),
        sessions: resolve(here, 'sessions.html'),
      },
    },
    // A stale dist must not linger next to a fresh build.
    emptyOutDir: true,
  },
})
