import { expect, test, type Page } from '@playwright/test'
import { readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

import { pickOption, shot, signIn } from './support'

/**
 * The model-service list: row order is pure storage, models are chips, and an
 * API key is typed once and never seen again — the config keeps a ${ENV}
 * reference and the plaintext lives in .env with owner-only permissions.
 * Adding and editing happen in a modal; every change round-trips the whole
 * stored list, and the default model is a named default_model the 设为默认
 * action retargets.
 */

const home = process.env.E2E_HOME ?? ''
const configPath = home ? join(home, 'forebrain.yaml') : ''
const envPath = home ? join(home, '.env') : ''

test.describe.configure({ mode: 'serial' })

async function rowOf(page: Page, name: string) {
  const index = await page.locator('[data-provider-row]').evaluateAll(
    (rows, want) => rows.findIndex((row) => row.getAttribute('data-provider-row') === want), name)
  return { index, row: page.locator(`[data-provider-row="${name}"]`) }
}

test('a new service with chips and a masked key saves', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  await page.click('[data-testid="providers-add"]')

  // The provider is a choice, never typed (owner 2026-10-21).
  await pickOption(page, 'provider-name', 'arcee-ai')
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
  // The stored key reads as the masked tail alone: whether it is saved is the
  // green badge's job, not a word in the value (owner 2026-10-21).
  await expect(page.locator('[data-testid="provider-key-saved"]')).toHaveText('••••9931')
  // The saved key and its 更换 button fill the field width, like every other
  // field in the form: the box stands where the input would be and the button
  // sits on the row's trailing edge (owner 2026-10-21).
  const savedKey = await page.locator('[data-testid="provider-key-saved"]').boundingBox()
  const changeKey = await page.locator('[data-testid="provider-key-change"]').boundingBox()
  const apiFormat = await page.locator('[data-testid="provider-api-format"]').boundingBox()
  expect(savedKey!.x).toBeCloseTo(apiFormat!.x, 0)
  // Both boxes are 36px tall, so the row reads as one control beside the rest.
  expect(savedKey!.height).toBeCloseTo(apiFormat!.height, 0)
  // The box, the 8px gap and the button end where the other fields end.
  expect(savedKey!.width + 8 + changeKey!.width).toBeCloseTo(apiFormat!.width, 0)
  await shot(page, 'providers-form-key-saved')
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

test('rows keep their stored order and the default is a named choice', async ({ page }) => {
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  await page.click('[data-testid="providers-add"]')
  await pickOption(page, 'provider-name', 'moonshotai')
  await page.fill('[data-testid="provider-base-url"]', 'http://127.0.0.1:8762')
  const chipsInput = page.locator('[data-testid="model-chips-input"]')
  await chipsInput.fill('kimi-e2e-model,')
  await chipsInput.blur()
  await page.fill('[data-testid="provider-key-input"]', 'sk-e2e-second-7788')
  await page.click('[data-testid="provider-save"]')
  await expect(page.locator('[data-provider-row="moonshotai"] [data-testid^="provider-key-saved-"]')).toContainText('7788', { timeout: 15_000 })

  // A new row is appended at the END and the stored order survives a reload:
  // the list is storage, not priority, and no operation reorders it.
  await expect(page.locator('[data-provider-row]').last()).toHaveAttribute('data-provider-row', 'moonshotai', { timeout: 15_000 })
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-provider-row]').last()).toHaveAttribute('data-provider-row', 'moonshotai')
  await shot(page, 'providers-form')

  // 设为默认 retargets default_model — badge moves, rows do not.
  await page.locator('[data-provider-row="moonshotai"]').locator('[data-testid^="provider-default-set-"]').click()
  // The badge is asserted by its class, not its text: the e2e gateway runs
  // the English locale (the zh wording is pinned by the unit tests).
  const badgeOf = (name: string) => page.locator(`[data-provider-row="${name}"]`).locator('.forebrain-badge--default')
  await expect(badgeOf('moonshotai')).toHaveCount(1, { timeout: 15_000 })
  expect(readFileSync(configPath, 'utf8')).toContain('default_model: moonshotai/kimi-e2e-model')
  await page.reload({ waitUntil: 'networkidle' })
  await expect(badgeOf('moonshotai')).toHaveCount(1)
  await expect(page.locator('[data-provider-row]').last()).toHaveAttribute('data-provider-row', 'moonshotai')

  // Remove the row this test added so the suite's own default is back for the
  // specs that follow. The row is the default right now, and deleting the
  // default row is refused — so the default goes back to the first other row
  // first.
  const firstOther = page.locator('[data-provider-row]:not([data-provider-row="moonshotai"])').first()
  const firstOtherName = await firstOther.getAttribute('data-provider-row')
  await firstOther.locator('[data-testid^="provider-default-set-"]').click()
  await expect(badgeOf(firstOtherName ?? '')).toHaveCount(1, { timeout: 15_000 })
  const { index } = await rowOf(page, 'moonshotai')
  await page.click(`[data-testid="provider-remove-${index}"]`)
  await page.click('[data-testid="confirm-ok"]')
  await expect(page.locator('[data-provider-row="moonshotai"]')).toHaveCount(0, { timeout: 15_000 })
})

test('the format is picked from a list and the params are edited as JSON', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })
  await page.click('[data-testid="providers-add"]')

  // No 高级 fold any more: the format and the params are fields of the form.
  await expect(page.locator('.forebrain-modal details')).toHaveCount(0)
  const format = page.locator('[data-testid="provider-api-format"]')
  await expect(format).toHaveAttribute('role', 'combobox')
  await format.click()
  await expect(page.getByRole('option')).toHaveText([
    'Anthropic Messages (/v1/messages)',
    'Chat Completions (/chat/completions)',
    'Responses (/responses)',
  ])
  // The list closes on the choice that is already in effect, so the form is
  // left as it was before the assertion.
  await page.getByRole('option', { name: 'Chat Completions (/chat/completions)', exact: true }).click()

  // Each field on its own row: the endpoint is never squeezed next to the
  // provider dropdown (owner 2026-10-21).
  const providerBox = await page.locator('[data-testid="provider-name"]').boundingBox()
  const baseUrlBox = await page.locator('[data-testid="provider-base-url"]').boundingBox()
  expect(baseUrlBox!.y).toBeGreaterThan(providerBox!.y + providerBox!.height - 1)

  // A provider the model catalog knows: the server refuses a name it cannot
  // build a client for, and the row that proved order above is already gone.
  await pickOption(page, 'provider-name', 'moonshotai')
  // Picking the provider fills in the endpoint it is served at; the suite's
  // own gateway stands in for the real one, so the field is then overwritten.
  await expect(page.locator('[data-testid="provider-base-url"]')).toHaveValue('https://api.moonshot.ai/v1')
  await page.fill('[data-testid="provider-base-url"]', 'http://127.0.0.1:8762')
  const chipsInput = page.locator('[data-testid="model-chips-input"]')
  await chipsInput.fill('kimi-e2e-format,')
  await chipsInput.blur()
  await page.fill('[data-testid="provider-key-input"]', 'sk-e2e-format-3311')

  await pickOption(page, 'provider-api-format', 'Anthropic Messages (/v1/messages)')
  // The params field is a JSON code editor: the text is typed into its content.
  const params = page.locator('[data-testid="provider-params-editor"] .cm-content')
  await params.click()
  await page.keyboard.type('{"temperature":0.3}')
  await expect(page.locator('[data-testid="provider-params-editor"]')).toContainText('temperature')
  await expect(page.locator('[data-testid="provider-params-error"]')).toHaveCount(0)
  await shot(page, 'providers-form-format')
  await page.click('[data-testid="provider-save"]')
  await expect(page.locator('[data-provider-row="moonshotai"]')).toHaveCount(1, { timeout: 15_000 })

  // The two answers land where the engine reads them.
  const config = readFileSync(configPath, 'utf8')
  expect(config).toContain('api_path: /v1/messages')
  expect(config).toContain('temperature')

  // Leave the suite's providers as they were for the specs that follow.
  const { index } = await rowOf(page, 'moonshotai')
  await page.click(`[data-testid="provider-remove-${index}"]`)
  await page.click('[data-testid="confirm-ok"]')
  await expect(page.locator('[data-provider-row="moonshotai"]')).toHaveCount(0, { timeout: 15_000 })
})

/**
 * A column's width belongs to the person at the screen (plan 084, owner
 * 2026-10-21): the header's trailing edge is a handle — drag it to widen the
 * column, double-click it to fit the column to its content — and both are
 * forgotten on a reload, because widths live for the session only.
 *
 * These are the only honest assertions about the feature: jsdom has no layout,
 * so the pixels can only be checked in a real browser.
 */
test('a column widens on a drag, fits its content on a double-click and is forgotten on reload', async ({ page }) => {
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })

  const wrap = page.locator('.forebrain-table-wrap')
  const heads = page.locator('.forebrain-table thead th')
  // 服务商 / 模型 / Base URL / API Key / 操作.
  const models = heads.nth(1)
  const baseUrl = heads.nth(2)
  const handle = models.locator('[data-col-resizer]')
  const widthOf = async (locator: typeof models): Promise<number> => {
    const box = await locator.boundingBox()
    if (!box) throw new Error('the column has no box')
    return box.width
  }

  await expect(wrap).toHaveCount(1)
  expect(await page.locator('[data-provider-row]').count()).toBeGreaterThan(0)

  const startModels = await widthOf(models)
  const startBaseUrl = await widthOf(baseUrl)
  // The design fills the box: nothing to scroll for yet.
  expect(await wrap.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true)

  // Drag the handle 80px to the right; the column follows the pointer and the
  // column after it keeps its width.
  const grip = await handle.boundingBox()
  if (!grip) throw new Error('the width handle has no box')
  const gripX = grip.x + grip.width / 2
  const gripY = grip.y + grip.height / 2
  await page.mouse.move(gripX, gripY)
  await page.mouse.down()
  await page.mouse.move(gripX + 80, gripY, { steps: 10 })
  await page.mouse.up()

  const draggedModels = await widthOf(models)
  expect(Math.abs(draggedModels - (startModels + 80))).toBeLessThanOrEqual(2)
  expect(Math.abs((await widthOf(baseUrl)) - startBaseUrl)).toBeLessThanOrEqual(1)
  // Wider than the box now, so the box scrolls instead of squeezing the cells.
  expect(await wrap.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true)
  await handle.hover()
  await shot(page, 'providers-column-dragged')

  // A double-click on the same handle fits the column to its content: narrower
  // than what the drag left, and never below the column minimum.
  await handle.dblclick()
  const fitted = await widthOf(models)
  expect(fitted).toBeLessThan(draggedModels - 20)
  expect(fitted).toBeGreaterThanOrEqual(56)
  await shot(page, 'providers-column-fitted')

  // The widths are the session's, not the user's: a reload is the design again.
  await page.reload({ waitUntil: 'networkidle' })
  expect(Math.abs((await widthOf(page.locator('.forebrain-table thead th').nth(1))) - startModels)).toBeLessThanOrEqual(2)
})

/**
 * Emptying the key field of a key-mode service is a clear, and a clear is
 * refused (owner 2026-10-21): the row would save and then answer 401 the moment
 * the chain fell back to it. The refusal is painted by the dialog that raised
 * it — the page banner sits under the mask the dialog paints — and nothing is
 * written.
 */
test('a refused key clear reports inside the dialog and writes nothing', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })

  const beforeConfig = readFileSync(configPath, 'utf8')
  const beforeEnv = readFileSync(envPath, 'utf8')

  // Row 0 is the suite's primary: a key-mode service with a stored key.
  await page.click('[data-testid="provider-edit-0"]')
  await page.click('[data-testid="provider-key-change"]')
  await expect(page.locator('[data-testid="provider-key-input"]')).toHaveValue('')
  await page.click('[data-testid="provider-save"]')

  const refusal = page.locator('[data-testid="provider-save-error"]')
  await expect(refusal).toBeVisible({ timeout: 15_000 })
  await expect(refusal).toContainText('needs api_key')
  // The dialog stayed open — the user's edits are still there to fix.
  await expect(page.locator('.forebrain-modal')).toBeVisible()

  // The sentence is above the mask, not merely present in the DOM: hit-testing
  // the block's own centre lands on the block. Under the mask it would land on
  // the mask.
  expect(
    await refusal.evaluate((el) => {
      const r = el.getBoundingClientRect()
      return document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)?.closest('[data-testid="provider-save-error"]') !== null
    }),
  ).toBe(true)
  // One copy only: a page-level banner carrying the same sentence would make
  // this two.
  await expect(page.getByText('needs api_key')).toHaveCount(1)
  await shot(page, 'providers-save-refused')

  // Cancel leaves the files exactly as they were: a refused clear is not a
  // write.
  await page.click('[data-testid="provider-cancel"]')
  await expect(page.locator('.forebrain-modal')).toHaveCount(0)
  expect(readFileSync(configPath, 'utf8')).toBe(beforeConfig)
  expect(readFileSync(envPath, 'utf8')).toBe(beforeEnv)

  // The row is still usable, and the refusal does not greet the next open.
  await page.click('[data-testid="provider-edit-0"]')
  await expect(page.locator('[data-testid="provider-key-saved"]')).toBeVisible()
  await expect(page.locator('[data-testid="provider-save-error"]')).toHaveCount(0)
  await page.click('[data-testid="provider-cancel"]')
})
