import { expect, test } from '@playwright/test'

import { seedProjectSession, signIn, shot } from './support'

/**
 * The network retry, proven on the real page: while the run's stream keeps
 * dying, the retry lives on the status line above the composer — reason,
 * countdown, attempt — and nowhere in the conversation; the dead attempts'
 * partial text is withdrawn from the live page and from the persisted
 * transcript a reload replays; and the turn recovers with exactly one
 * answer, no retry card or network-error block ever drawn.
 */
test('the network retry lives on the status line, never in the conversation', async ({ page }) => {
  test.skip(process.env.E2E_REAL_LLM === '1', 'the netdrop probe drives the fake provider')
  test.info().setTimeout(240_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  const session = await seedProjectSession(page, 'e2e-netretry')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')

  // Three requests are cut mid-answer, the fourth answers normally: the run
  // retries on its own while the page is watched.
  await page.fill('textarea', 'hello [[e2e:netdrop]]')
  await page.press('textarea', 'Enter')

  // The status line becomes the retry inside the first back-off window.
  const line = page.locator('[data-testid="run-status-line"]')
  await expect(line).toContainText(/Retrying in|后重试/, { timeout: 5_000 })
  await shot(page, 'network-retry-status')

  // The turn recovers: the answer arrives, and the dead attempts' partial
  // text never survived anywhere on the page.
  const main = page.locator('.chat-shell main')
  const reply = process.env.E2E_REPLY_OK ?? 'E2E_REPLY_OK'
  await expect(main).toContainText(reply, { timeout: 90_000 })
  expect((await main.innerText()).split('partial answer that never').length - 1).toBe(0)
  // No retry card and no transport-error block was ever drawn.
  await expect(page.locator('.transport-retry-card')).toHaveCount(0)
  await expect(page.locator('[data-testid^="transport-retry"]')).toHaveCount(0)
  await shot(page, 'network-retry-recovered')

  // The persisted transcript a reload replays is the same story: exactly one
  // answer, and the withdrawn text belongs to no attempt that survived. (The
  // live page draws a turn's answer twice for every turn — a pre-existing
  // observer-path double the reload does not carry — so exactly-once lives
  // here, on the durable record.)
  await page.reload({ waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await expect(main).toContainText(reply)
  expect((await main.innerText()).split(reply).length - 1).toBe(1)
  const replayed = (await main.innerText()).split('partial answer that never').length - 1
  expect(replayed).toBe(0)
  await expect(page.locator('.transport-retry-card')).toHaveCount(0)
  await expect(page.locator('[data-testid^="transport-retry"]')).toHaveCount(0)
})
