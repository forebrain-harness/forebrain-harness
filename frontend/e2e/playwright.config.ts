import { defineConfig } from '@playwright/test'

// Driven by scripts/acceptance/web_e2e.sh against a real gateway binary; see
// that script for the environment every variable below expects.
export default defineConfig({
  testDir: '.',
  workers: 1,
  timeout: 60_000,
  reporter: [['list']],
  use: {
    baseURL: process.env.E2E_BASE_URL,
    // The locally installed Google Chrome by default; on machines without it,
    // set E2E_BROWSER_CHANNEL=chromium (after installing the browser).
    channel: process.env.E2E_BROWSER_CHANNEL ?? 'chrome',
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
  },
  outputDir: process.env.E2E_SHOTS + '/artifacts',
})
