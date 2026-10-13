import { expect, test } from '@playwright/test'

import { signIn } from './support'

/**
 * Plan 015's page structure, over every page: the content opens with its one
 * top row — never an h1 title layer — and no page wears the current primary
 * agent's badge (the rail's selector already names it). The run also captures
 * one 1440×900 screenshot per page for the review.
 */
const PAGES = [
  '/',
  '/projects',
  '/rules',
  '/skills',
  '/workshop',
  '/subagents',
  '/cron',
  '/channels',
  '/providers',
  '/tools',
  '/permissions',
  '/memories',
  '/settings',
]

test('no page carries a title layer or a primary-agent badge', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)
  for (const path of PAGES) {
    await page.goto(path, { waitUntil: 'networkidle' })
    await expect(page.locator('main h1')).toHaveCount(0)
    await expect(page.locator('.scope-badge--agent')).toHaveCount(0)
    await expect(page.locator('[data-testid="scope-badge"]')).toHaveCount(0)
    const name = path === '/' ? 'chat' : path.slice(1).replace(/\//g, '-')
    await page.screenshot({ path: `${process.env.E2E_SHOTS}/flat-${name}.png` })
  }
})
