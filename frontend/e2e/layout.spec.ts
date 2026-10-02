import { expect, test, type Page } from '@playwright/test'

import { expectAssistantReply, signIn } from './support'

async function openDrawer(page: Page) {
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await expect(page.locator('[data-testid="chat-drawer"]')).toBeVisible()
}

test('chat page opens with the rail and the conversation only', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await expect(page.locator('.forebrain-rail')).toBeVisible()
  await expect(page.locator('.chat-shell main')).toBeVisible()
  await expect(page.locator('[data-testid="chat-workbench"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="chat-drawer"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="workbench-toggle"]')).toHaveAttribute('aria-pressed', 'false')
  const body = await page.evaluate(() => document.body.innerText)
  expect(body).not.toContain('管控')
  expect(body).not.toContain('工作区')
  const viewport = page.viewportSize()
  const mainBox = await page.locator('.chat-shell').boundingBox()
  expect(Math.abs((mainBox?.x ?? 0) + (mainBox?.width ?? 0) - (viewport?.width ?? 0))).toBeLessThanOrEqual(1)
})

test('workbench opens as the third column and closes again', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-testid="workbench-toggle"]')
  await page.click('[data-testid="workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible({ timeout: 10_000 })
  const viewport = page.viewportSize()
  const rail = await page.locator('.forebrain-rail').boundingBox()
  const main = await page.locator('.chat-shell main').boundingBox()
  const bench = await page.locator('[data-testid="chat-workbench"]').boundingBox()
  expect(Math.abs((rail?.x ?? 0) + (rail?.width ?? 0) - (main?.x ?? 0))).toBeLessThanOrEqual(1)
  expect(Math.abs((main?.x ?? 0) + (main?.width ?? 0) - (bench?.x ?? 0))).toBeLessThanOrEqual(1)
  expect(Math.abs((bench?.x ?? 0) + (bench?.width ?? 0) - (viewport?.width ?? 0))).toBeLessThanOrEqual(1)
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/layout-workbench-open.png`, fullPage: true })
  await page.locator('.chat-workbench-close').click()
  await expect(page.locator('[data-testid="chat-workbench"]')).toHaveCount(0)
  // Stays open across pages within the tab, resets on reload.
  await page.click('[data-testid="workbench-toggle"]')
  // Cross-page persistence is the SPA's own navigation; page.goto is a full
  // load and resets by design.
  await page.click('.forebrain-rail a[href="/subagents"]')
  await page.waitForURL(/subagents/)
  await page.click('.forebrain-rail a[href="/"]')
  await page.waitForURL((url) => url.pathname === '/')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible()
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="chat-workbench"]')).toHaveCount(0)
})

test('chat menu opens the drawer and new chat lives there', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await openDrawer(page)
  const rail = await page.locator('.forebrain-rail').boundingBox()
  const drawer = await page.locator('[data-testid="chat-drawer"]').boundingBox()
  expect(Math.abs((drawer?.x ?? 0) - ((rail?.x ?? 0) + (rail?.width ?? 0)))).toBeLessThanOrEqual(1)
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/layout-drawer.png`, fullPage: true })
  await page.click('[data-testid="drawer-new-chat"]')
  await expect(page.locator('[data-testid="chat-drawer"]')).toHaveCount(0)
  await expect(page).toHaveURL(/session=/)
  await openDrawer(page)
  await expect(page.locator('.chat-drawer-row--active')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-testid="chat-drawer"]')).toHaveCount(0)
})

test('sending a message and switching sessions', async ({ page }) => {
  // Two full model turns plus drawer round-trips exceed the default minute.
  test.info().setTimeout(240_000)
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await openDrawer(page)
  await page.click('[data-testid="drawer-new-chat"]')
  await page.waitForURL(/session=/)
  await page.waitForSelector('textarea')
  const firstId = new URL(page.url()).searchParams.get('session')
  await page.fill('textarea', 'first-e2e')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  // The gateway may hand back the just-touched session when a create races
  // the previous turn's writes; take another until it is a different one.
  let secondId = firstId
  for (let attempt = 0; attempt < 5 && secondId === firstId; attempt++) {
    await openDrawer(page)
    await page.click('[data-testid="drawer-new-chat"]')
    // The click's navigation is async; typing before it lands would send
    // into the previous conversation. Wait for the id to actually change.
    await page.waitForFunction(
      (previous) => {
        const current = new URL(window.location.href).searchParams.get('session')
        return current && current !== previous
      },
      firstId!,
      { timeout: 10_000 },
    )
    await page.waitForSelector('textarea')
    secondId = new URL(page.url()).searchParams.get('session')
  }
  expect(secondId).not.toBe(firstId)
  await page.fill('textarea', 'second-e2e')
  await page.press('textarea', 'Enter')
  // In suite order the second turn can trail the first by a wide margin on
  // the shared gateway; wait on the full pair, not just any reply text
  // (the first turn's reply still matches the same constant).
  await page.waitForFunction(
    () => document.body.innerText.includes('second-e2e')
      && Array.from(document.querySelectorAll('[role=log] .is-assistant'))
        .some((n) => (n.textContent ?? '').includes('E2E_REPLY_OK')),
    undefined,
    { timeout: 150_000, polling: 500 },
  )
  await openDrawer(page)
  await page.locator('.chat-drawer-row', { hasText: 'first' }).first().click()
  await expect(page).toHaveURL(new RegExp(`session=${firstId}`))
  await expect(page.locator('.chat-shell main')).toContainText('first-e2e')
  const body = await page.evaluate(() => document.body.innerText)
  expect(body).not.toContain('second-e2e')
})

test('heartbeat is in the rail, not the workbench', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await openDrawer(page)
  await page.click('[data-testid="drawer-new-chat"]')
  await page.click('[data-testid="workbench-toggle"]')
  const benchText = await page.locator('[data-testid="chat-workbench"]').innerText()
  expect(benchText).not.toContain('心跳')
  await page.locator('[data-testid="chat-workbench"] .chat-workbench-close').click()
  await page.locator('.forebrain-rail-foot button').first().click()
  await expect(page.locator('.rail-popover')).toBeVisible()
  await page.fill('.rail-popover input[type="number"]', '30')
  await page.fill('.rail-popover textarea', '有新变化吗')
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/layout-heartbeat.png`, fullPage: true })
  await page.locator('.rail-popover button', { hasText: /启用|Start|Update|更新/ }).first().click()
  await page.keyboard.press('Escape')
  await expect(page.locator('.rail-popover')).toHaveCount(0)
  await openDrawer(page)
  await expect(page.locator('[data-testid="drawer-heartbeat-indicator"]')).toBeVisible()
})

test('empty live agents card is hidden', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.click('[data-testid="workbench-toggle"]')
  const benchText = await page.locator('[data-testid="chat-workbench"]').innerText()
  expect(benchText).not.toContain('运行中的代理')
  expect(benchText).not.toContain('Live agents')
})

test('rail collapses to icons and starts expanded', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  let box = await page.locator('.forebrain-rail').boundingBox()
  expect(Math.abs((box?.width ?? 0) - 264)).toBeLessThanOrEqual(1)
  await expect(page.locator('[data-testid="rail-collapse"]').first()).toHaveAttribute('aria-expanded', 'true')
  await page.locator('[data-testid="rail-collapse"]').first().click()
  box = await page.locator('.forebrain-rail').boundingBox()
  expect(Math.abs((box?.width ?? 0) - 64)).toBeLessThanOrEqual(1)
  const firstIcon = page.locator('.forebrain-rail-group > button.forebrain-rail-link').first()
  await firstIcon.hover()
  await expect(page.locator('[role="tooltip"]')).toBeVisible({ timeout: 300 })
  const iconBox = await firstIcon.boundingBox()
  const tipBox = await page.locator('[role="tooltip"]').boundingBox()
  expect(tipBox?.x ?? 0).toBeGreaterThan((iconBox?.x ?? 0) + (iconBox?.width ?? 0))
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/layout-rail-collapsed.png`, fullPage: true })
  await openDrawer(page)
  const drawer = await page.locator('[data-testid="chat-drawer"]').boundingBox()
  expect(Math.abs((drawer?.x ?? 0) - 64)).toBeLessThanOrEqual(1)
  await page.reload({ waitUntil: 'networkidle' })
  box = await page.locator('.forebrain-rail').boundingBox()
  expect(Math.abs((box?.width ?? 0) - 264)).toBeLessThanOrEqual(1)
})

test('run closes with the terminal worked line', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.fill('textarea', 'worked-line-e2e')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  const line = await page.locator('.chat-shell main').innerText()
  expect(line).toMatch(/已工作 \d+s[\s\S]*?\d{2}:\d{2}|Worked for \d+s[\s\S]*?\d{2}:\d{2}/)
})
