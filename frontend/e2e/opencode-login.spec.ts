import { expect, test, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { shot, signIn } from './support'

/**
 * Signing a model service in from the model-settings page. The flows themselves
 * run in the gateway (they need a device code and, for ChatGPT, a loopback
 * callback — neither belongs to a browser page), so this spec stubs the three
 * login endpoints and the catalog listing and drives the page: the device code,
 * the authorization link, the catalog the finished login unlocks, and the row
 * that lands in the config once a model is chosen.
 *
 * The stubs answer in the gateway's own shape (snake_case JSON, 202 for a start,
 * 404 for an unknown id), so the page's polling is exercised as it really runs.
 */

const home = process.env.E2E_HOME ?? ''
const configPath = home ? join(home, 'forebrain.yaml') : ''

test.describe.configure({ mode: 'serial' })

type LoginBody = {
  status: 'pending' | 'complete' | 'failed'
  url: string
  user_code: string
  error: string
  base_url: string
}

function pending(overrides: Partial<LoginBody> = {}): LoginBody {
  return { status: 'pending', url: '', user_code: '', error: '', base_url: '', ...overrides }
}

function complete(baseUrl: string): LoginBody {
  return { status: 'complete', url: '', user_code: '', error: '', base_url: baseUrl }
}

/**
 * Answers the three login endpoints for one provider: a 202 with an id, then the
 * given statuses in order (the last one repeats, so a slow poll never runs off
 * the end), and a 204 for the cancel a closed modal sends.
 */
async function stubLogin(page: Page, provider: string, statuses: LoginBody[]): Promise<void> {
  let polls = 0
  await page.route('**/api/auth/login/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/cancel')) {
      await route.fulfill({ status: 204, body: '' })
      return
    }
    if (route.request().method() === 'POST') {
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ login_id: `${provider}-login-1`, provider }),
      })
      return
    }
    const body = statuses[Math.min(polls, statuses.length - 1)]
    polls += 1
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })
}

type CatalogModel = { id: string; name: string; free?: boolean }

/** Answers the catalog listing for one provider and lets every other listing
 * through to the gateway. */
async function stubCatalog(page: Page, provider: string, models: CatalogModel[]): Promise<void> {
  await page.route('**/api/models**', async (route) => {
    const asked = new URL(route.request().url()).searchParams.get('provider') ?? ''
    if (asked !== provider) {
      await route.fallback()
      return
    }
    const records = models.map((model, index) => ({
      model_id: `${provider}/${model.id}`,
      model_name: model.free ? `${model.name} (free)` : model.name,
      provider,
      api_model: model.id,
      context_window: 128000,
      can_reason: false,
      supports_attachments: false,
      is_default: index === 0,
      source: 'opencode-account',
    }))
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ records, status: [] }) })
  })
}

/** The page opens the authorization URL through window.open; the run must not
 * reach the real authorize page, so the call is recorded and nothing loads. */
async function recordWindowOpen(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const opened: string[] = []
    ;(window as unknown as { __openedAuthorizations: string[] }).__openedAuthorizations = opened
    window.open = ((url?: string | URL) => {
      opened.push(String(url ?? ''))
      return null
    }) as typeof window.open
  })
}

test('signing in to OpenCode shows the device code, then the catalog it unlocks', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await recordWindowOpen(page)
  // The answer the gateway gives for this login: the Go subscription's root,
  // because that is the surface the row it writes talks to. The page writes the
  // answer as it is, so the spec has to stub it faithfully.
  await stubLogin(page, 'opencode', [
    pending(),
    pending({ url: 'https://opencode.ai/console/device?user_code=ABCD-1234', user_code: 'ABCD-1234' }),
    complete('https://opencode.ai/inference/go'),
  ])
  await stubCatalog(page, 'opencode-go', [
    { id: 'glm-5.3', name: 'GLM 5.3' },
    { id: 'kimi-k3', name: 'Kimi K3' },
  ])
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })

  await page.click('[data-testid="providers-connect-opencode"]')
  // One way in: the Go device flow starts with the modal.

  const code = page.locator('[data-testid="provider-login-code"]')
  await expect(code).toHaveText('ABCD-1234', { timeout: 15_000 })
  await shot(page, 'providers-open-login-code')

  await page.click('[data-testid="provider-login-open"]')
  expect(await page.evaluate(() => (window as unknown as { __openedAuthorizations: string[] }).__openedAuthorizations))
    .toEqual(['https://opencode.ai/console/device?user_code=ABCD-1234'])

  const model = page.locator('[data-testid="provider-login-model-kimi-k3"]')
  await expect(model).toBeVisible({ timeout: 15_000 })
  await model.click()
  await shot(page, 'providers-open-login-catalog')
  await page.click('[data-testid="provider-login-save"]')

  // The row the sign-in wrote, and the Go root its catalog is served from.
  const row = page.locator('[data-provider-row="opencode-go"]')
  await expect(row).toBeVisible({ timeout: 15_000 })
  await expect(row).toContainText('kimi-k3')
  await expect(row).toContainText('https://opencode.ai/inference/go')
  // A signed-in service carries no API key: the credential is the login's.
  await expect(row.locator('[data-testid^="provider-key-unset-"]')).toBeVisible()

  // Written to the config, not only to the page.
  await expect.poll(() => readFileSync(configPath, 'utf8'), { timeout: 15_000 }).toContain('opencode-go')
  expect(readFileSync(configPath, 'utf8')).toContain('https://opencode.ai/inference/go')

  // And it survives a reload: the write round-tripped through the gateway.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-provider-row="opencode-go"]')).toContainText('kimi-k3')
})

test('signing in to ChatGPT shows the authorization link, then its account catalog', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await recordWindowOpen(page)
  const authorize = 'https://auth.openai.com/oauth/authorize?client_id=app_e2e&state=xyz'
  await stubLogin(page, 'chatgpt', [pending({ url: authorize }), complete('https://chatgpt.com/backend-api/codex')])
  await stubCatalog(page, 'chatgpt', [{ id: 'gpt-5.6', name: 'GPT-5.6' }])
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })

  await page.click('[data-testid="providers-connect-chatgpt"]')
  // ChatGPT has one way in, so the flow starts with the modal.
  await expect(page.locator('[data-testid="provider-login-open"]')).toBeEnabled({ timeout: 15_000 })
  await page.click('[data-testid="provider-login-open"]')
  expect(await page.evaluate(() => (window as unknown as { __openedAuthorizations: string[] }).__openedAuthorizations))
    .toEqual([authorize])

  const model = page.locator('[data-testid="provider-login-model-gpt-5.6"]')
  await expect(model).toBeVisible({ timeout: 15_000 })
  await page.click('[data-testid="provider-login-save"]')

  const row = page.locator('[data-provider-row="chatgpt"]')
  await expect(row).toBeVisible({ timeout: 15_000 })
  await expect(row).toContainText('gpt-5.6')
  // The endpoint comes from the login's own completion answer.
  await expect(row).toContainText('https://chatgpt.com/backend-api/codex')
})

test('a reusable saved login skips the flow and opens the catalog directly', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await recordWindowOpen(page)
  // The start answer a machine with a live login gets: complete with the base
  // URL and no login_id — there is nothing to poll and no code to confirm.
  await page.route('**/api/auth/login/opencode', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.fallback()
      return
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ provider: 'opencode', status: 'complete', base_url: 'https://opencode.ai/inference/go' }),
    })
  })
  await stubCatalog(page, 'opencode-go', [
    { id: 'glm-5.3', name: 'GLM 5.3' },
    { id: 'kimi-k3', name: 'Kimi K3' },
  ])
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })

  await page.click('[data-testid="providers-connect-opencode"]')

  // No device code step, no authorization to open — the catalog is the first
  // thing on the screen, holding the listing the page itself fetched.
  await expect(page.locator('[data-testid="provider-login-models"]')).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('[data-testid="provider-login-code"]')).toHaveCount(0)
  expect(await page.evaluate(() => (window as unknown as { __openedAuthorizations: string[] }).__openedAuthorizations))
    .toEqual([])
  await expect(page.locator('[data-testid="provider-login-model-glm-5.3"]')).toBeVisible()

  await page.click('[data-testid="provider-login-model-kimi-k3"]')
  await page.click('[data-testid="provider-login-save"]')
  const row = page.locator('[data-provider-row="opencode-go"]')
  await expect(row).toBeVisible({ timeout: 15_000 })
  await expect(row).toContainText('kimi-k3')
  await expect(row).toContainText('https://opencode.ai/inference/go')
})

test('a complete answer whose catalog fetch fails offers the same retry, not a login', async ({ page }) => {
  test.skip(!home, 'E2E_HOME missing')
  await recordWindowOpen(page)
  await page.route('**/api/auth/login/opencode', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.fallback()
      return
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ provider: 'opencode', status: 'complete', base_url: 'https://opencode.ai/inference/go' }),
    })
  })
  await page.route('**/api/models**', async (route) => {
    const asked = new URL(route.request().url()).searchParams.get('provider') ?? ''
    if (asked !== 'opencode-go') {
      await route.fallback()
      return
    }
    await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'unavailable' }) })
  })
  await signIn(page)
  await page.goto('/providers', { waitUntil: 'networkidle' })

  await page.click('[data-testid="providers-connect-opencode"]')

  // The listing failed, but the login stands: the page reports the failure
  // with its own retry — no device code, no authorization link.
  await expect(page.locator('[data-testid="provider-login-error"]')).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('[data-testid="provider-login-retry"]')).toBeVisible()
  await expect(page.locator('[data-testid="provider-login-code"]')).toHaveCount(0)
  expect(await page.evaluate(() => (window as unknown as { __openedAuthorizations: string[] }).__openedAuthorizations))
    .toEqual([])
})
