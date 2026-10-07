import { configDefaults, defineConfig } from 'vitest/config'
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
    // e2e/ holds Playwright specs that drive a real browser and a real
    // gateway; vitest must not try to run them in jsdom.
    exclude: [...configDefaults.exclude, 'e2e/**'],
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
      '/api': {
        target: 'http://127.0.0.1:6060',
        // Cookie-authenticated writes must name the gateway's own origin (the
        // same Origin-vs-Host check the socket handshake applies); the dev
        // server stands in for that origin, so it says so.
        changeOrigin: true,
        configure: (proxy) => {
          proxy.on('proxyReq', (proxyReq) => {
            if (proxyReq.getHeader('origin')) proxyReq.setHeader('origin', 'http://127.0.0.1:6060')
          })
        },
      },
      '/ws': {
        target: 'ws://127.0.0.1:6060',
        ws: true,
        // The gateway's default origin check compares Origin against Host;
        // rewrite both to the target so the proxied handshake passes it.
        changeOrigin: true,
        configure: (proxy) => {
          proxy.on('proxyReqWs', (proxyReq) => proxyReq.setHeader('origin', 'http://127.0.0.1:6060'))
        },
      },
    },
  },
})
