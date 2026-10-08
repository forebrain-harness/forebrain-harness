import { readFileSync } from 'fs'
import { expect, test } from '@playwright/test'

/**
 * The startup banner the gateway prints on stdout (captured to
 * E2E_GATEWAY_OUT by web_e2e.sh): it must name the service, say where the Web
 * UI and the gateway listen, and never leak the token into a non-terminal
 * output. The route table this banner once listed was removed on purpose
 * (owner decision, 2026-10-08); what stays asserted is what an operator needs
 * to reach the service.
 */
test('gateway start prints banner, web UI and listen address', () => {
  const outPath = process.env.E2E_GATEWAY_OUT
  if (!outPath) throw new Error('E2E_GATEWAY_OUT must point at the gateway stdout capture')
  const out = readFileSync(outPath, 'utf-8')
  const base = process.env.E2E_BASE_URL ?? ''

  expect(out).toContain('|  ___|__  _ __ ___| |__')
  expect(out).toContain('Forebrain Harness Gateway')

  expect(out).toContain(`Web UI       ${base}/`)
  expect(out).toContain('Auth         token')
  expect(out).toContain(`Listening on ${base}`)

  // stdout was redirected to a file, not a terminal: no token link, no slog noise.
  expect(out).not.toContain(process.env.E2E_TOKEN ?? '')
  expect(out).not.toContain('level=')
})
