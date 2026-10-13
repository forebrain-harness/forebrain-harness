import { expect, test } from '@playwright/test'
import { readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

import { shot, signIn } from './support'

/**
 * The model-service list: order is priority, models are chips, and an API key
 * is typed once and never seen again — the config keeps a ${ENV} reference and
 * the plaintext lives in .env with owner-only permissions. Adding and editing
 * happen in a modal; every change round-trips the whole ordered list.
 */

const home = process.env.E2E_HOME ?? ''
const configPath = home ? join(home, 'forebrain.yaml') : ''
const envPath = home ? join(home, '.env') : ''

test.describe.configure({ mode: 'serial' })

async function rowOf(page: Parameters<typeof signIn>[0], name: string) {
  const index = await page.locator('[data-provider-row]').evaluateAll(
    (rows, want) => rows.findIndex((row) => row.getAttribute('data-provider-row') === want), name)
  return { index, row: page.locator(`[data-provider-row="${name}"]`) }
}

test('a new service with chips and a masked key saves', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  await page.click('[data-testid="providers-add"]')

  await page.fill('[data-testid="provider-name"]', 'arcee-ai')
  await page.fill('[data-testid="provider-key-input"]', 'sk-e2e-secret-9931')

  // The key input is a password field: what the page shows is masked.
  await expect(page.locator('[data-testid="provider-key-input"]')).toHaveAttribute('type', 'password')

  const chipsInput = page.locator('[data-testid="model-chips-input"]')
  await chipsInput.fill('e2e-model-a, e2e-model-b')
  await chipsInput.blur()
  await expect(page.locator('[data-testid="model-chips"] [data-chip="e2e-model-a"]')).toBeVisible()
  await expect(page.locator('[data-testid="model-chips"] [data-chip="e2e-model-b"]')).toBeVisible()

  await page.click('[data-testid="provider-save"]')
  // The saved badge is the reloaded state: it appears only after the PUT
  // round-tripped and the page refetched, which is when the files hold the
  // new row.
  await expect(page.locator('[data-provider-row="arcee-ai"] [data-testid^="provider-key-saved-"]')).toContainText('9931', { timeout: 15_000 })

  // The yaml holds a reference and no plaintext; .env holds the plaintext
  // with owner-only permissions.
  const config = readFileSync(configPath, 'utf8')
  expect(config).toContain('${ARCEE_AI_API_KEY}')
  expect(config).not.toContain('sk-e2e-secret-9931')
  const env = readFileSync(envPath, 'utf8')
  expect(env).toContain('sk-e2e-secret-9931')
  expect(statSync(envPath).mode & 0o077).toBe(0)
})

test('reload shows the masked key and leaks nothing', async ({ page }) => {
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  const saved = page.locator('[data-provider-row="arcee-ai"] [data-testid^="provider-key-saved-"]')
  await expect(saved).toBeVisible({ timeout: 10_000 })
  await expect(saved).toContainText('9931')
  // The key never travels back in any form: not plaintext, not a placeholder.
  const body = await page.content()
  expect(body).not.toContain('sk-e2e-secret-9931')
  expect(body).not.toContain('[REDACTED]')
})

test('editing models without touching the key keeps .env unchanged', async ({ page }) => {
  const before = readFileSync(envPath, 'utf8')
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  const { index } = await rowOf(page, 'arcee-ai')
  await page.click(`[data-testid="provider-edit-${index}"]`)
  const chipsInput = page.locator('[data-testid="model-chips-input"]')
  await chipsInput.fill('e2e-model-c,')
  await chipsInput.blur()
  await expect(page.locator('[data-testid="model-chips"] [data-chip="e2e-model-c"]')).toBeVisible()
  await page.click('[data-testid="provider-save"]')
  await expect(page.locator('[data-provider-row="arcee-ai"]')).toContainText('e2e-model-c', { timeout: 15_000 })
  await page.waitForTimeout(300)
  expect(readFileSync(envPath, 'utf8')).toBe(before)
})

test('changing the key rotates .env', async ({ page }) => {
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  const { index } = await rowOf(page, 'arcee-ai')
  await page.click(`[data-testid="provider-edit-${index}"]`)
  await page.click('[data-testid="provider-key-change"]')
  await page.fill('[data-testid="provider-key-input"]', 'sk-e2e-rotated-4455')
  await page.click('[data-testid="provider-save"]')
  await expect(page.locator('[data-provider-row="arcee-ai"] [data-testid^="provider-key-saved-"]')).toContainText('4455', { timeout: 15_000 })
  const env = readFileSync(envPath, 'utf8')
  expect(env).toContain('sk-e2e-rotated-4455')
  expect(env).not.toContain('sk-e2e-secret-9931')
})

test('row order is the fallback order and persists', async ({ page }) => {
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  await page.click('[data-testid="providers-add"]')
  await page.fill('[data-testid="provider-name"]', 'moonshotai')
  // It becomes the primary below, and a primary must be complete — the
  // server refuses one that would keep the gateway from starting again.
  await page.fill('[data-testid="provider-base-url"]', 'http://127.0.0.1:8762')
  const chipsInput = page.locator('[data-testid="model-chips-input"]')
  await chipsInput.fill('kimi-e2e-model,')
  await chipsInput.blur()
  await page.fill('[data-testid="provider-key-input"]', 'sk-e2e-second-7788')
  await page.click('[data-testid="provider-save"]')
  await expect(page.locator('[data-provider-row="moonshotai"] [data-testid^="provider-key-saved-"]')).toContainText('7788', { timeout: 15_000 })

  // Move the new row above every existing one: it becomes the primary and
  // stays there after a reload. The chain's length is whatever the suite's
  // config offers, so the move is counted from the row's own position.
  const indexOf = (name: string) => page.locator('[data-provider-row]').evaluateAll(
    (rows, want) => rows.findIndex((row) => row.getAttribute('data-provider-row') === want), name)
  const up = page.locator('[data-provider-row="moonshotai"]').locator('[data-testid^="provider-up-"]')
  for (let i = await indexOf('moonshotai'); i > 0; i--) await up.click()
  await expect(page.locator('[data-provider-row]').first()).toHaveAttribute('data-provider-row', 'moonshotai', { timeout: 15_000 })
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-provider-row]').first()).toHaveAttribute('data-provider-row', 'moonshotai')
  await shot(page, 'providers-form')

  // Remove the row this test added so the suite's own primary is first again
  // for the specs that follow; the move has already proven order persists.
  await page.click('[data-testid="provider-remove-0"]')
  await page.click('[data-testid="confirm-ok"]')
  await expect(page.locator('[data-provider-row="moonshotai"]')).toHaveCount(0, { timeout: 15_000 })
})
