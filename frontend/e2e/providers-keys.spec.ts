import { expect, test } from '@playwright/test'
import { readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

import { shot, signIn } from './support'

/**
 * The model-service form: order is priority, models are chips, and an API key
 * is typed once and never seen again — the config keeps a ${ENV} reference and
 * the plaintext lives in .env with owner-only permissions.
 */

const home = process.env.E2E_HOME ?? ''
const configPath = home ? join(home, 'forebrain.yaml') : ''
const envPath = home ? join(home, '.env') : ''

test.describe.configure({ mode: 'serial' })

test('a new service with chips and a masked key saves', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  await page.click('[data-testid="providers-add"]')

  const rows = page.locator('[data-provider-row]')
  const newRow = rows.last()
  await newRow.locator('[data-testid^="provider-name-"]').fill('arcee-ai')
  await newRow.locator('[data-testid^="provider-key-input-"]').fill('sk-e2e-secret-9931')

  // The key input is a password field: what the page shows is masked.
  await expect(newRow.locator('[data-testid^="provider-key-input-"]')).toHaveAttribute('type', 'password')

  const chipsInput = newRow.locator('[data-testid="model-chips-input"]')
  await chipsInput.fill('e2e-model-a, e2e-model-b')
  await chipsInput.blur()
  await expect(newRow.locator('[data-chip="e2e-model-a"]')).toBeVisible()
  await expect(newRow.locator('[data-chip="e2e-model-b"]')).toBeVisible()

  await page.click('[data-testid="providers-save"]')
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
  const row = page.locator('[data-provider-row="arcee-ai"]')
  await row.locator('[data-testid="model-chips-input"]').fill('e2e-model-c')
  await row.locator('[data-testid="model-chips-input"]').blur()
  await expect(row.locator('[data-chip="e2e-model-c"]')).toBeVisible()
  await page.click('[data-testid="providers-save"]')
  await expect(page.locator('[data-provider-row="arcee-ai"]')).toContainText('e2e-model-c', { timeout: 15_000 })
  await page.waitForTimeout(300)
  expect(readFileSync(envPath, 'utf8')).toBe(before)
  await expect(page.locator('[data-provider-row="arcee-ai"]')).toContainText('e2e-model-c')
})

test('changing the key rotates .env', async ({ page }) => {
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  const row = page.locator('[data-provider-row="arcee-ai"]')
  await row.locator('[data-testid^="provider-key-change-"]').click()
  await row.locator('[data-testid^="provider-key-input-"]').fill('sk-e2e-rotated-4455')
  await page.click('[data-testid="providers-save"]')
  await expect(page.locator('[data-provider-row="arcee-ai"] [data-testid^="provider-key-saved-"]')).toContainText('4455', { timeout: 15_000 })
  const env = readFileSync(envPath, 'utf8')
  expect(env).toContain('sk-e2e-rotated-4455')
  expect(env).not.toContain('sk-e2e-secret-9931')
})

test('row order is the fallback order and persists', async ({ page }) => {
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  await page.click('[data-testid="providers-add"]')
  const newRow = page.locator('[data-provider-row]').last()
  await newRow.locator('[data-testid^="provider-name-"]').fill('moonshotai')
  // It becomes the primary below, and a primary must be complete — the
  // server refuses one that would keep the gateway from starting again.
  await newRow.locator('[data-testid^="provider-base-url-"]').fill('http://127.0.0.1:8762')
  await newRow.locator('[data-testid="model-chips-input"]').fill('kimi-e2e-model,')
  await newRow.locator('[data-testid^="provider-key-input-"]').fill('sk-e2e-second-7788')
  await page.click('[data-testid="providers-save"]')
  await expect(page.locator('[data-provider-row="moonshotai"] [data-testid^="provider-key-saved-"]')).toContainText('7788', { timeout: 15_000 })

  // Move the new row above both existing ones: it becomes the primary and
  // stays there after a reload.
  const up = page.locator('[data-provider-row="moonshotai"]').locator('[data-testid^="provider-up-"]')
  await up.click()
  await up.click()
  await page.click('[data-testid="providers-save"]')
  await expect(page.locator('[data-provider-row]').first()).toHaveAttribute('data-provider-row', 'moonshotai', { timeout: 15_000 })
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-provider-row]').first()).toHaveAttribute('data-provider-row', 'moonshotai')
  await shot(page, 'providers-form')

  // Put the suite's own primary back first: later specs talk to it.
  const down = page.locator('[data-provider-row="moonshotai"]').locator('[data-testid^="provider-down-"]')
  await down.click()
  await down.click()
  await page.click('[data-testid="providers-save"]')
  await expect(page.locator('[data-provider-row]').last()).toHaveAttribute('data-provider-row', 'moonshotai', { timeout: 15_000 })
})
