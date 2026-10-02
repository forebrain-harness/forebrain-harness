import { expect, test } from '@playwright/test'

import { expectAssistantReply, shot, signIn, watch401 } from './support'

const PAGES = [
  '/',
  '/projects',
  '/subagents',
  '/cron',
  '/channels',
  '/permissions',
  '/tools',
  '/providers',
  '/memories',
  '/settings',
]

function gatewayHost(): string {
  return (process.env.E2E_BASE_URL ?? '').replace(/^https?:\/\//, '')
}

test('unauthenticated visit lands on the sign-in page', async ({ page }) => {
  const unauthorized = watch401(page)
  // The page follows the browser locale; pin it so the toggle assertion below
  // tests the switch, not whatever Chrome's default happened to be.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await page.goto('/')
  await expect(page).toHaveURL(/\/login\?redirect=%2F$|\/login\?redirect=\/$/)
  await expect(page.getByRole('heading', { level: 2 })).toBeVisible()
  // No request other than the session probe may fail with 401.
  expect(unauthorized()).toEqual([])
  // The footer names the gateway actually answering.
  await expect(page.locator('.lp-foot code')).toContainText(gatewayHost())
  // An empty token keeps the submit button disabled.
  await expect(page.locator('button[type=submit]')).toBeDisabled()
  // The language switch is the page's only chrome.
  await page.click('.lp-lang')
  await expect(page.getByRole('heading', { level: 2 })).toHaveText('Sign in')
  await shot(page, '01-login-page')
})

test('wrong token is refused with the server message', async ({ page }) => {
  await page.goto('/login')
  await page.fill('#gateway-token', 'not-the-token')
  await page.click('button[type=submit]')
  await expect(page.locator('.lp-error')).toHaveText('invalid gateway token')
  await expect(page).toHaveURL(/\/login/)
  await shot(page, '02-login-wrong-token')
})

test('sign-in unlocks every page without 401', async ({ page }) => {
  await signIn(page)
  const unauthorized = watch401(page)
  const okPaths: string[] = []
  page.on('response', (response) => {
    if (response.status() === 200) okPaths.push(new URL(response.url()).pathname)
  })
  for (const path of PAGES) {
    await page.goto(path, { waitUntil: 'networkidle' })
  }
  // The endpoints behind the merged settings tabs fire when each tab opens.
  await page.goto('/settings', { waitUntil: 'networkidle' })
  for (const label of [/主代理|Primary agents/, /MCP/, /钩子|Hooks/, /记忆开关|Memory switches/]) {
    await page.locator('[data-testid="settings-tabs"] button', { hasText: label }).click()
    await page.waitForLoadState('networkidle')
  }
  expect(unauthorized()).toEqual([])
  // The surfaces the original 401 report named all answered 200 — the
  // memories page now lists files, which is its request.
  for (const probe of ['/api/providers', '/api/hooks', '/api/memories/files']) {
    expect(okPaths, `${probe} must have answered 200`).toContain(probe)
  }
  await shot(page, '03-pages-after-signin')
})

test('session survives a reload and stays out of script reach', async ({ page }) => {
  await signIn(page)
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page).toHaveURL('/')
  const cookies = await page.context().cookies()
  const sessionCookie = cookies.find((cookie) => cookie.name.startsWith('forebrain_session_'))
  expect(sessionCookie).toBeTruthy()
  expect(sessionCookie?.httpOnly).toBe(true)
  expect(sessionCookie?.sameSite).toBe('Strict')
  const documentCookie: string = await page.evaluate(() => document.cookie)
  expect(documentCookie.includes('forebrain_session_')).toBe(false)
  await shot(page, '04-session-cookie')
})

test('one-click link signs in and scrubs the token from the address bar', async ({ page }) => {
  const token = process.env.E2E_TOKEN ?? ''
  await page.goto(`/login#token=${encodeURIComponent(token)}`)
  await page.waitForURL((url) => url.pathname === '/')
  expect(page.url()).not.toContain('token')
  await shot(page, '05-login-link')
})

test('chat streams over the cookie-authenticated websocket', async ({ page }) => {
  await signIn(page)
  await page.fill('textarea', 'ping')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  await shot(page, '06-chat-websocket')
})

test('uploaded image reaches the api through the cookie', async ({ page }) => {
  await signIn(page)
  await page.setInputFiles('input[type=file]', {
    name: 'dot.png',
    mimeType: 'image/png',
    buffer: Buffer.from(
      'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
      'base64',
    ),
  })
  await page.fill('textarea', 'ping')
  await page.press('textarea', 'Enter')
  const attachment = page.locator('a[href*="/api/files/"]').first()
  await expect(attachment).toBeVisible({ timeout: 30_000 })
  // The attachment URL is a plain resource URL: it must load on the session
  // cookie alone, with no header the page could have added.
  const href = await attachment.getAttribute('href')
  expect(href).toBeTruthy()
  const status = await page.evaluate(async (url) => {
    const res = await fetch(url)
    return { status: res.status, type: res.headers.get('content-type') ?? '' }
  }, href)
  expect(status.status).toBe(200)
  expect(status.type.startsWith('image/')).toBe(true)
  await shot(page, '07-attachment-cookie')
})
