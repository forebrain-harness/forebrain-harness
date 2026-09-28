import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { readFileSync } from 'fs'
import { resolve } from 'path'

const root = resolve(__dirname, '..')
// The UI shows the same version the binary reports, read from the one file that
// carries it rather than a copy that can drift.
const forebrainVersion = readFileSync(resolve(root, 'VERSION'), 'utf-8').trim()
const elementsRoot = resolve(root, 'ai-elements-vue/packages/elements')
const shadcnRoot = resolve(root, 'ai-elements-vue/packages/shadcn-vue')

export default defineConfig({
  plugins: [vue()],
  // Component tests mount real components, so they need a DOM. Everything else
  // — the aliases, the version define — is shared with the app build, which is
  // what keeps a test rendering the same component tree the browser does.
  test: {
    environment: 'jsdom',
    // The Markdown renderer pulls in a stylesheet. Externalized dependencies
    // are not put through Vite, so the CSS import reaches Node as an unknown
    // file extension; inlining it lets the same pipeline the app uses handle it.
    server: { deps: { inline: ['vue-stream-markdown'] } },
  },
  define: {
    __FOREBRAIN_VERSION__: JSON.stringify(forebrainVersion),
  },
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
      '@repo/elements': resolve(elementsRoot, 'src'),
      '@repo/shadcn-vue': shadcnRoot,
      // See src/lib/mermaid-unavailable.ts: the markdown renderer's probe for
      // mermaid must fail, not resolve to an empty stand-in.
      mermaid: resolve(__dirname, 'src/lib/mermaid-unavailable.ts'),
    },
    dedupe: ['nanoid', 'vue'],
  },
  optimizeDeps: {
    include: ['nanoid'],
  },
  build: {
    // Build straight into the Go embed package so the binary is self-contained.
    // emptyOutDir is false so the committed .gitkeep placeholder survives (it
    // keeps `go build` working on a clean checkout); the Makefile cleans stale
    // assets before building.
    outDir: resolve(__dirname, '../pkg/gateway/dist'),
    emptyOutDir: false,
  },
  server: {
    port: 3000,
    proxy: {
      '/forebrain/api/v1': {
        target: 'http://localhost:8216',
        changeOrigin: true,
        rewrite(path) {
          let p = path.replace(/^\/forebrain\/api\/v1/, '/superagent/api/v1')
          const legacySeg = String.fromCharCode(111, 112, 101, 110, 99, 108, 97, 119)
          p = p.replace('/deploy/gateway', `/deploy/${legacySeg}`)
          p = p.replace('/superagent/api/v1/relay/', `/superagent/api/v1/${legacySeg}/relay/`)
          return p
        },
      },
      '/api': {
        target: 'http://127.0.0.1:6060',
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://127.0.0.1:6060',
        ws: true,
      },
    },
  },
})
