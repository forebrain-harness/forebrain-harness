import { expect, test } from '@playwright/test'

import { signIn } from './support'

/** The seal mark everywhere it appears: login card, rail (both themes), and
 * the static favicon in its fixed navy. */
test('seal mark renders in favicon, login and rail', async ({ page }) => {
  // 1. Static favicon: navy, flat.
  const favicon = await page.request.get('/favicon.svg')
  expect(favicon.status()).toBe(200)
  const body = await favicon.text()
  expect(body).toContain('#1F4A9E')
  expect(body).not.toContain('Gradient')
  await page.goto('/login')
  await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href', '/favicon.svg')

  // 2. Login page: brand-coloured seal with white glyph.
  const loginMark = page.locator('.lp-mark')
  await expect(loginMark).toBeVisible()
  const loginFills = await page.evaluate(() => {
    const svg = document.querySelector('.lp-mark') as SVGSVGElement | null
    const square = svg?.querySelector('rect[rx="7"]')
    const glyph = svg?.querySelector('path')
    return {
      square: square ? getComputedStyle(square).fill : null,
      glyph: glyph ? getComputedStyle(glyph).fill : null,
    }
  })
  expect(loginFills.square).toBe('rgb(31, 74, 158)')
  expect(loginFills.glyph).toBe('rgb(255, 255, 255)')
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/brand-login.png`, fullPage: true })

  // 3. Rail: inverted on dark, brand-coloured on light.
  await signIn(page)
  await page.evaluate(() => localStorage.setItem('forebrain-theme', 'dark'))
  await page.goto('/', { waitUntil: 'networkidle' })
  const darkFills = await page.evaluate(() => {
    const svg = document.querySelector('.forebrain-rail-mark') as SVGSVGElement | null
    const square = svg?.querySelector('rect[rx="7"]')
    const glyph = svg?.querySelector('path')
    return {
      square: square ? getComputedStyle(square).fill : null,
      glyph: glyph ? getComputedStyle(glyph).fill : null,
    }
  })
  expect(darkFills.square).toBe('rgb(255, 255, 255)')
  expect(darkFills.glyph).toBe('rgb(15, 29, 56)')
  const rail = page.locator('.forebrain-rail')
  await rail.screenshot({ path: `${process.env.E2E_SHOTS}/brand-rail-dark.png` })

  await page.evaluate(() => localStorage.setItem('forebrain-theme', 'light'))
  await page.goto('/', { waitUntil: 'networkidle' })
  const lightFill = await page.evaluate(() => {
    const svg = document.querySelector('.forebrain-rail-mark') as SVGSVGElement | null
    const square = svg?.querySelector('rect[rx="7"]')
    return square ? getComputedStyle(square).fill : null
  })
  expect(lightFill).toBe('rgb(31, 74, 158)')
  await rail.screenshot({ path: `${process.env.E2E_SHOTS}/brand-rail-light.png` })

  // 4. The seal follows the brand scheme.
  await page.evaluate(() => localStorage.setItem('forebrain-brand', 'teal'))
  await page.goto('/', { waitUntil: 'networkidle' })
  const tealFill = await page.evaluate(() => {
    const svg = document.querySelector('.forebrain-rail-mark') as SVGSVGElement | null
    const square = svg?.querySelector('rect[rx="7"]')
    return square ? getComputedStyle(square).fill : null
  })
  expect(tealFill).toBe('rgb(14, 110, 126)')
  await page.evaluate(() => {
    document.documentElement.dataset.brand = 'navy'
    localStorage.setItem('forebrain-brand', 'navy')
  })
})
