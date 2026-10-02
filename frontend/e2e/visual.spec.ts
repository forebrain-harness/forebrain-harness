import { expect, test } from '@playwright/test'

import { signIn } from './support'

/**
 * The visual system: the content area is always white, colour is flat (no
 * gradients, no glass), the rail carries the only dark/light theme, and the
 * three brand schemes recolour every accent in unison.
 */

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

const RAIL_BG = {
  dark: 'rgb(15, 29, 56)',
  light: 'rgb(243, 245, 249)',
}

const TENANT_MENU_BG = {
  dark: 'rgb(19, 36, 71)',
  light: 'rgb(255, 255, 255)',
}

async function assertFlatAndWhite(page: import('@playwright/test').Page, railTheme: 'dark' | 'light') {
  const bodyBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  expect(bodyBg).toBe('rgb(255, 255, 255)')

  const workBg = await page.evaluate(() => {
    const work = document.querySelector('.forebrain-work')
    return work ? getComputedStyle(work).backgroundColor : null
  })
  expect(workBg).toBe('rgb(255, 255, 255)')

  const railBg = await page.evaluate(() => {
    const rail = document.querySelector('.forebrain-rail')
    return rail ? getComputedStyle(rail).backgroundColor : null
  })
  expect(railBg).toBe(RAIL_BG[railTheme])

  const tenantMenu = await page.evaluate(async (theme) => {
    const trigger = document.querySelector('.forebrain-tenant-trigger') as HTMLElement | null
    if (!trigger) return null
    trigger.click()
    await new Promise((resolve) => setTimeout(resolve, 120))
    const menu = document.querySelector('.forebrain-tenant-menu') as HTMLElement | null
    return menu ? getComputedStyle(menu).backgroundColor : null
  }, railTheme)
  expect(tenantMenu).toBe(TENANT_MENU_BG[railTheme])

  const offenders = await page.evaluate(() => {
    const gradient: string[] = []
    const backdrop: string[] = []
    for (const el of Array.from(document.querySelectorAll('*'))) {
      const style = getComputedStyle(el)
      if (style.backgroundImage.includes('gradient')) gradient.push(el.tagName + '.' + el.className.toString().slice(0, 40))
      if (style.backdropFilter !== 'none') backdrop.push(el.tagName + '.' + el.className.toString().slice(0, 40))
    }
    return { gradient, backdrop }
  })
  expect(offenders.gradient).toEqual([])
  expect(offenders.backdrop).toEqual([])

  const hasDarkClass = await page.evaluate(() => document.documentElement.classList.contains('dark'))
  expect(hasDarkClass).toBe(false)

  const fontFamily = await page.evaluate(() => getComputedStyle(document.body).fontFamily)
  expect(fontFamily.startsWith('-apple-system')).toBe(true)
}

test('every page is flat and white under both rail themes', async ({ page }) => {
  await signIn(page)
  for (const railTheme of ['dark', 'light'] as const) {
    await page.evaluate((theme) => localStorage.setItem('forebrain-theme', theme), railTheme)
    for (const path of PAGES) {
      await page.goto(path, { waitUntil: 'networkidle' })
      await assertFlatAndWhite(page, railTheme)
      const name = path === '/' ? 'chat' : path.replaceAll('/', '').replaceAll(':', '-')
      await page.screenshot({ path: `${process.env.E2E_SHOTS}/visual-${name}-navy-${railTheme}.png`, fullPage: true })
    }
  }
})

const SCHEME_RADIOS: Record<string, string> = {
  // Playwright's Chrome may run in either locale; pick the radio by its
  // swatch's data-scheme instead of its translated label.
  navy: '海军蓝 / Navy',
  teal: '松石青 / Teal',
  graphite: '石墨黑 / Graphite',
}

async function pickScheme(page: import('@playwright/test').Page, scheme: 'navy' | 'teal' | 'graphite') {
  await page.locator(`.brand-swatch[data-scheme="${scheme}"]`).click()
}

test('brand scheme switch recolours the app and persists', async ({ page }) => {
  await signIn(page)
  await page.goto('/settings', { waitUntil: 'networkidle' })

  const cases = [
    { brand: 'navy', btn: 'rgb(31, 74, 158)', rail: 'rgb(15, 29, 56)' },
    { brand: 'teal', btn: 'rgb(14, 110, 126)', rail: 'rgb(13, 36, 41)' },
    { brand: 'graphite', btn: 'rgb(28, 28, 31)', rail: 'rgb(20, 20, 22)' },
  ] as const

  await page.evaluate(() => localStorage.setItem('forebrain-theme', 'dark'))
  for (const c of cases) {
    await pickScheme(page, c.brand)
    await expect(page.locator('html')).toHaveAttribute('data-brand', c.brand)
    const swatchBg = await page.evaluate((scheme) => {
      const el = document.querySelector(`.brand-swatch[data-scheme="${scheme}"]`) as HTMLElement | null
      return el ? getComputedStyle(el).backgroundColor : null
    }, c.brand)
    expect(swatchBg).toBe(c.btn)
    await page.goto('/', { waitUntil: 'networkidle' })
    const railBg = await page.evaluate(() => {
      const rail = document.querySelector('.forebrain-rail')
      return rail ? getComputedStyle(rail).backgroundColor : null
    })
    expect(railBg).toBe(c.rail)
    await page.screenshot({ path: `${process.env.E2E_SHOTS}/visual-chat-${c.brand}-dark.png`, fullPage: true })
    await page.evaluate(() => localStorage.setItem('forebrain-theme', 'light'))
    await page.goto('/', { waitUntil: 'networkidle' })
    await page.screenshot({ path: `${process.env.E2E_SHOTS}/visual-chat-${c.brand}-light.png`, fullPage: true })
    await page.evaluate(() => localStorage.setItem('forebrain-theme', 'dark'))
    await page.goto('/settings', { waitUntil: 'networkidle' })
  }

  // Persistence: teal survives a reload, then restore navy for the other specs.
  await pickScheme(page, 'teal')
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('html')).toHaveAttribute('data-brand', 'teal')
  await pickScheme(page, 'navy')
})
