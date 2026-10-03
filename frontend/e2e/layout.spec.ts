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
  // The second conversation is new, so its own reply — and, with a real
  // model whose words are unknown, its closing worked line — is the turn.
  await page.waitForFunction(
    (realModel) => document.body.innerText.includes('second-e2e')
      && (realModel
        ? /已工作 \d|Worked for \d/.test(document.querySelector('.chat-shell main')?.textContent ?? '')
        : Array.from(document.querySelectorAll('[role=log] .is-assistant'))
          .some((n) => (n.textContent ?? '').includes('E2E_REPLY_OK'))),
    process.env.E2E_REAL_LLM === '1',
    { timeout: 150_000, polling: 500 },
  )
  await openDrawer(page)
  await page.locator('.chat-drawer-row', { hasText: 'first' }).first().click()
  await expect(page).toHaveURL(new RegExp(`session=${firstId}`))
  // What the user sent in each conversation is the check: a real model's
  // reply may quote anything, including the other conversation's word.
  await expect(page.locator('[role=log] .is-user', { hasText: 'first-e2e' })).toHaveCount(1)
  await expect(page.locator('[role=log] .is-user', { hasText: 'second-e2e' })).toHaveCount(0)

  // Back to the conversation the last message went into: its history loads
  // again (a send that finished before the reload is part of what it reads).
  await openDrawer(page)
  await page.locator('.chat-drawer-row', { hasText: 'second' }).first().click()
  await expect(page).toHaveURL(new RegExp(`session=${secondId}`))
  await expect(page.locator('[role=log] .is-user', { hasText: 'second-e2e' })).toHaveCount(1, { timeout: 15_000 })
  await expect(page.locator('[role=log] .is-user', { hasText: 'first-e2e' })).toHaveCount(0)
})

test('the drawer lists conversations when opened away from the chat page', async ({ page }) => {
  await signIn(page)
  const created = await page.request.post('/api/chat/sessions', { data: { title: 'drawer-elsewhere-e2e' } })
  expect(created.ok()).toBeTruthy()
  // A fresh load on another page: the chat page never mounted to fetch.
  await page.goto('/skills', { waitUntil: 'networkidle' })
  await openDrawer(page)
  await expect(page.locator('.chat-drawer-row', { hasText: 'drawer-elsewhere-e2e' }).first()).toBeVisible({ timeout: 10_000 })
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
  // The terminal's duration spelling: 12s, 1m 12s or 1h 02m 03s.
  expect(line).toMatch(/(已工作|Worked for) (\d+h )?(\d+m )?\d+s[\s\S]*?\d{2}:\d{2}/)
  // A reload rebuilds the conversation from history, and every run closes
  // with its one line there too: as many lines as were drawn live.
  const lines = page.locator('[data-testid="run-worked-line"]')
  const live = await lines.count()
  expect(live).toBeGreaterThan(0)
  // The page reopens the conversation it was on.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('.chat-shell main')).toContainText('worked-line-e2e', { timeout: 10_000 })
  await expect(lines).toHaveCount(live, { timeout: 10_000 })
  await expect(lines.last()).toContainText(/(已工作|Worked for) (\d+h )?(\d+m )?\d+s/)
  // The session's workspace panel reads as words: no payload printed raw.
  // Only the panel's own summary toggles it — nested context-debug details
  // carry summaries of their own.
  const workspace = page.locator('[data-testid="session-workspace"]')
  await workspace.locator('> summary').click()
  await expect(workspace).toContainText(/(成本摘要|Cost summary)/)
  await expect(workspace).not.toContainText('{"')
})
