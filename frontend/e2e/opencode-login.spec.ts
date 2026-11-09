import { expect, test, type Page } from '@playwright/test'

import { shot, signIn } from './support'

/**
 * Subscription sign-in from the models page (plan 266): one modal for every
 * subscription, three steps — pick it, sign in to it, choose the models it
 * unlocked.
 *
 * The flows themselves run in the gateway (they need a device code and, for
 * ChatGPT, a loopback callback — neither belongs to a browser page), so this
 * spec stubs the registry, the login endpoints, the offers and the write, and
 * drives the page through both flow kinds. The stubs answer in the gateway's own
 * shape (snake_case JSON, 202 for a start, 409 with the keys a plan holds), so
 * the page's polling and its refusal branches are exercised as they really run.
 */

test.describe.configure({ mode: 'serial' })

type LoginBody = {
  status: 'pending' | 'complete' | 'failed'
  url: string
  user_code: string
  error: string
  base_url: string
  flow: string
}

function pending(overrides: Partial<LoginBody> = {}): LoginBody {
  return { status: 'pending', url: '', user_code: '', error: '', base_url: '', flow: '', ...overrides }
}

function complete(baseUrl: string, flow: string): LoginBody {
  return { status: 'complete', url: '', user_code: '', error: '', base_url: baseUrl, flow }
}

/** One subscription as the registry lists it. */
type Sub = { id: string; name: string; flows: string[]; state: string }

async function stubSubscriptions(page: Page, subs: Sub[]): Promise<void> {
  await page.route('**/api/subscriptions', async (route) => {
    if (route.request().method() !== 'GET') {
      await route.fallback()
      return
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(subs) })
  })
}

/**
 * Answers one subscription's login endpoints: a 202 with an id, then the given
 * statuses in order (the last one repeats, so a slow poll never runs off the
 * end), and a 204 for the cancel a closed modal sends.
 */
async function stubLogin(page: Page, id: string, statuses: LoginBody[]): Promise<void> {
  let polls = 0
  await page.route('**/api/auth/login/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/cancel')) {
      await route.fulfill({ status: 204, body: '' })
      return
    }
    if (url.pathname.endsWith('/callback')) {
      await route.fallback()
      return
    }
    const asked = url.pathname.split('/api/auth/login/')[1]?.split('/')[0] ?? ''
    if (asked !== id) {
      await route.fallback()
      return
    }
    if (route.request().method() === 'POST') {
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ login_id: `${id}-login-1`, provider: id }),
      })
      return
    }
    const body = statuses[Math.min(polls, statuses.length - 1)]
    polls += 1
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })
}

/** What a finished sign-in offers: one group per plan, one row per model. */
async function stubOffers(page: Page, id: string, offers: Record<string, unknown>[]): Promise<void> {
  await page.route(`**/api/subscriptions/${id}/offers`, async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(offers) })
  })
}

/** The write that turns chosen offers into rows. */
async function stubProvision(page: Page, id: string, ids: string[]): Promise<void> {
  await page.route(`**/api/subscriptions/${id}/provision`, async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ids }) })
  })
}

function offer(plan: string, model: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    plan,
    plan_name: plan,
    provider: 'opencode-go',
    model,
    base_url: 'https://opencode.ai/zen/go/v1',
    api_path: '/chat/completions',
    endpoint: 'https://opencode.ai/zen/go/v1/chat/completions',
    context_window: 1_000_000,
    supports_images: false,
    default: false,
    from_static_list: false,
    ...extra,
  }
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

async function openSignIn(page: Page): Promise<void> {
  // The product's own copy is Chinese-first; these assertions quote it.
  await page.addInitScript(() => {
    try {
      localStorage.setItem('forebrain-locale', 'zh')
    } catch {
      // A context with no storage still gets the page's own default.
    }
  })
  await signIn(page)
  await page.goto('/settings?tab=models', { waitUntil: 'networkidle' })
  await page.click('[data-testid="models-subscribe"]')
}

test('a device-code sign-in shows its code, then the models it unlocked', async ({ page }) => {
  await recordWindowOpen(page)
  await stubSubscriptions(page, [{ id: 'opencode-go', name: 'OpenCode Go', flows: ['device_code'], state: 'missing' }])
  await stubLogin(page, 'opencode-go', [
    pending(),
    pending({ url: 'https://opencode.ai/console/device?user_code=ABCD-1234', user_code: 'ABCD-1234', flow: 'device_code' }),
    complete('https://opencode.ai/zen/go/v1', 'device_code'),
  ])
  await stubOffers(page, 'opencode-go', [offer('Go 订阅', 'glm-5.3'), offer('Go 订阅', 'kimi-k3')])
  await stubProvision(page, 'opencode-go', ['row-glm', 'row-kimi'])

  let provisioned: { offers?: { model: string }[] } = {}
  page.on('request', (request) => {
    if (request.url().includes('/provision') && request.method() === 'POST') {
      provisioned = JSON.parse(request.postData() ?? '{}') as { offers?: { model: string }[] }
    }
  })

  await openSignIn(page)
  await expect(page.locator('[data-testid="models-subscribe-step"]')).toContainText('第 1 步')
  await shot(page, 'models-subscribe-step1')

  // Choosing it starts the flow and moves to step 2.
  await page.click('[data-subscription="opencode-go"]')
  await expect(page.locator('[data-testid="models-subscribe-step"]')).toContainText('第 2 步')
  await expect(page.locator('[data-testid="models-login-code"]')).toHaveText('ABCD-1234', { timeout: 15_000 })
  await shot(page, 'models-subscribe-device-code')

  await page.click('[data-testid="models-login-open"]')
  expect(await page.evaluate(() => (window as unknown as { __openedAuthorizations: string[] }).__openedAuthorizations))
    .toEqual(['https://opencode.ai/console/device?user_code=ABCD-1234'])

  // The poll's complete answer lands on step 3: the vendor's own listing.
  await expect(page.locator('[data-testid="models-subscribe-step"]')).toContainText('第 3 步', { timeout: 15_000 })
  await expect(page.locator('[data-offer="0"]')).toBeVisible()
  await expect(page.locator('[data-plan]')).toContainText('Go 订阅')
  await shot(page, 'models-subscribe-offers')

  await page.click('[data-offer="1"]')
  await page.click('[data-testid="models-provision"]')

  // The modal closes on success and the write carried exactly the chosen rows.
  await expect(page.locator('[data-testid="models-provision"]')).toHaveCount(0, { timeout: 15_000 })
  expect(provisioned.offers?.map((chosen) => chosen.model)).toEqual(['kimi-k3'])
})

test('a paste-callback sign-in asks for the address from the address bar', async ({ page }) => {
  await recordWindowOpen(page)
  await stubSubscriptions(page, [{ id: 'chatgpt', name: 'ChatGPT', flows: ['paste_callback'], state: 'missing' }])
  await stubLogin(page, 'chatgpt', [
    pending({ url: 'https://auth.openai.com/oauth/authorize?client_id=app_e2e', flow: 'paste_callback' }),
  ])
  let pasted = ''
  await page.route('**/api/auth/login/chatgpt/**/callback', async (route) => {
    pasted = String((JSON.parse(route.request().postData() ?? '{}') as { url?: string }).url ?? '')
    await route.fulfill({ status: 202, body: '' })
  })

  await openSignIn(page)
  await page.click('[data-subscription="chatgpt"]')

  await expect(page.locator('[data-testid="models-login-open"]')).toBeEnabled({ timeout: 15_000 })
  await page.click('[data-testid="models-login-open"]')
  expect(await page.evaluate(() => (window as unknown as { __openedAuthorizations: string[] }).__openedAuthorizations))
    .toEqual(['https://auth.openai.com/oauth/authorize?client_id=app_e2e'])

  // No device code for this flow, and the address has somewhere to go.
  await expect(page.locator('[data-testid="models-login-code"]')).toHaveCount(0)
  await page.fill('[data-testid="models-login-paste"]', 'http://localhost:1455/auth/callback?code=abc')
  await shot(page, 'models-subscribe-paste')
  await page.click('[data-testid="models-login-paste-submit"]')
  await expect.poll(() => pasted).toBe('http://localhost:1455/auth/callback?code=abc')
})

test('a plan holding several keys asks which one under its own group', async ({ page }) => {
  await stubSubscriptions(page, [{ id: 'opencode-go', name: 'OpenCode Go', flows: ['device_code'], state: 'usable' }])
  await stubOffers(page, 'opencode-go', [offer('Go 订阅', 'glm-5.3')])
  let asked = 0
  await page.route('**/api/subscriptions/opencode-go/provision', async (route) => {
    asked += 1
    if (asked === 1) {
      await route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify({
          error: '这个套餐里有多把 Key，请选择一把',
          key_choice: [
            { id: 'key-1', name: 'api-key-20261102', tail: 'd410' },
            { id: 'key-2', name: 'team-ci', tail: '7b03' },
          ],
        }),
      })
      return
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ids: ['row-glm'] }) })
  })

  await openSignIn(page)
  // Already signed in: the modal opens on the models it unlocked, not on a flow.
  await page.click('[data-subscription="opencode-go"]')
  await expect(page.locator('[data-testid="models-subscribe-step"]')).toContainText('第 3 步')
  await expect(page.locator('[data-offer="0"]')).toBeVisible()

  await page.click('[data-offer="0"]')
  await page.click('[data-testid="models-provision"]')

  // The refusal names the keys, and the choice is made where the plan is drawn.
  await expect(page.locator('[data-testid="models-key-choice"]')).toBeVisible()
  await shot(page, 'models-subscribe-key-choice')
})

test('a signed-in subscription whose listing fails says so instead of showing models', async ({ page }) => {
  await stubSubscriptions(page, [{ id: 'opencode-go', name: 'OpenCode Go', flows: ['device_code'], state: 'usable' }])
  await page.route('**/api/subscriptions/opencode-go/offers', async (route) => {
    await route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({ error: '读取订阅的模型列表失败，请重新登录' }),
    })
  })

  await openSignIn(page)
  await page.click('[data-subscription="opencode-go"]')
  await expect(page.locator('[data-testid="models-offers-error"]')).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('[data-testid="models-offers-error"]')).toContainText('重新登录')
})
