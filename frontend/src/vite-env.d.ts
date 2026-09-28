/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_FOREBRAIN_GATEWAY_WS?: string
  readonly VITE_FOREBRAIN_GATEWAY_TOKEN?: string
  readonly VITE_FOREBRAIN_BOT_MENTIONS?: string
  readonly VITE_FOREBRAIN_GATEWAY_TIMEOUT_MS?: string
  readonly VITE_FOREBRAIN_ADMIN_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}

/** Injected at build time from the repository's VERSION file. */
declare const __FOREBRAIN_VERSION__: string
