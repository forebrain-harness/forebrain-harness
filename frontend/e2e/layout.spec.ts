import { expect, test, type Page } from '@playwright/test'

import { expectAssistantReply, seedProjectSession, signIn } from './support'

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
  // A new chat is the page itself (plan 072): no third column, and no toggle
  // for one either.
  await expect(page.locator('[data-testid="workbench-toggle"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="chat-greeting"]')).toBeVisible()
  const body = await page.evaluate(() => document.body.innerText)
  expect(body).not.toContain('管控')
  expect(body).not.toContain('工作区')
  const viewport = page.viewportSize()
  const mainBox = await page.locator('.chat-shell').boundingBox()
  expect(Math.abs((mainBox?.x ?? 0) + (mainBox?.width ?? 0) - (viewport?.width ?? 0))).toBeLessThanOrEqual(1)
})

test('workbench opens as the third column and closes again', async ({ page }) => {
  await signIn(page)
  // The column belongs to a conversation, so the page opens one.
  const session = await seedProjectSession(page, 'layout-bench')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
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
  // load and resets by design. The way back is history, not a load: the home
  // link now leads to the new chat, which closes the column on purpose.
  await page.click('.forebrain-rail a[href="/subagents"]')
  await page.waitForURL(/subagents/)
  await page.goBack()
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
  // A new chat opens the new-conversation page (plan 029): no conversation is
  // created until a project is picked and the first message is sent.
  await expect(page).not.toHaveURL(/session=/)
  await expect(page.locator('[data-testid="chat-greeting"]')).toBeVisible()
  await expect(page.locator('[data-testid="project-pill"]')).toBeVisible()
})

test('sending a message and switching sessions', async ({ page }) => {
  // Two full model turns plus navigation exceed the default minute.
  test.info().setTimeout(240_000)
  await signIn(page)
  // Every conversation belongs to a project (plan 029); open two of them.
  const firstId = await seedProjectSession(page, 'layout-first')
  await page.goto(`/?session=${firstId}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'first-e2e')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)

  const secondId = await seedProjectSession(page, 'layout-second')
  await page.goto(`/?session=${secondId}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'second-e2e')
  await page.press('textarea', 'Enter')
  // In suite order the second turn can trail the first by a wide margin on
  // the shared gateway; wait on the full pair, not just any reply text.
  await page.waitForFunction(
    (realModel) => document.body.innerText.includes('second-e2e')
      && (realModel
        ? /已工作 \d|Worked for \d/.test(document.querySelector('.chat-shell main')?.textContent ?? '')
        : Array.from(document.querySelectorAll('[role=log] .is-assistant'))
          .some((n) => (n.textContent ?? '').includes('E2E_REPLY_OK'))),
    process.env.E2E_REAL_LLM === '1',
    { timeout: 150_000, polling: 500 },
  )

  // Each conversation keeps its own history: switching shows only its own.
  await page.goto(`/?session=${firstId}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[role=log] .is-user', { hasText: 'first-e2e' })).toHaveCount(1, { timeout: 15_000 })
  await expect(page.locator('[role=log] .is-user', { hasText: 'second-e2e' })).toHaveCount(0)

  await page.goto(`/?session=${secondId}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[role=log] .is-user', { hasText: 'second-e2e' })).toHaveCount(1, { timeout: 15_000 })
  await expect(page.locator('[role=log] .is-user', { hasText: 'first-e2e' })).toHaveCount(0)
})

test('the drawer lists conversations when opened away from the chat page', async ({ page }) => {
  await signIn(page)
  // A conversation exists; since plan 029 it belongs to a project. The point
  // of this test is that the drawer loads its own list, off the chat page.
  await seedProjectSession(page, 'layout-elsewhere')
  await page.goto('/skills', { waitUntil: 'networkidle' })
  const listed = page.waitForResponse((res) => res.request().method() === 'GET' && res.url().includes('/api/chat/sessions'))
  await openDrawer(page)
  expect((await listed).ok()).toBe(true)
  await expect(page.locator('[data-testid="chat-drawer"]')).toBeVisible()
})

test('heartbeat lives in the conversation header, not the rail or the workbench', async ({ page }) => {
  await signIn(page)
  const session = await seedProjectSession(page, 'layout-heartbeat')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await expect(page.locator('.forebrain-rail')).not.toContainText(/心跳|Heartbeat/)
  await page.click('[data-testid="workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).not.toContainText(/心跳|Heartbeat/)
  await page.locator('[data-testid="chat-workbench"] .chat-workbench-close').click()
  await page.click('[data-testid="heartbeat-toggle"]')
  const popover = page.locator('[data-testid="heartbeat-popover"]')
  await expect(popover).toBeVisible()
  await popover.locator('input[type="number"]').fill('30')
  await popover.locator('textarea').fill('有新变化吗')
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/layout-heartbeat.png`, fullPage: true })
  await page.click('[data-testid="heartbeat-save"]')
  await expect(page.locator('[data-testid="heartbeat-status"]')).toHaveText(/每 30 分钟|Every 30 min/)
  await expect(popover.locator('[data-testid="heartbeat-schedule"]')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(popover).toHaveCount(0)
  // The drawer's own heartbeat mark is for a conversation the drawer lists;
  // the drawer spans every project's conversations now, but this one has
  // none while it is open. Removing the mark from the conversation's header
  // clears its status.
  await page.click('[data-testid="heartbeat-toggle"]')
  await page.click('[data-testid="heartbeat-clear"]')
  await expect(page.locator('[data-testid="heartbeat-status"]')).toHaveCount(0)
})

test('empty live agents card is hidden', async ({ page }) => {
  await signIn(page)
  const session = await seedProjectSession(page, 'layout-bench-empty')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
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
  const session = await seedProjectSession(page, 'layout-worked-line')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
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
  // The workspace panel moved off the chat page (plan 016): its content
  // belongs to the workbench's own tabs, so nothing sits above the composer.
  await expect(page.locator('[data-testid="session-workspace"]')).toHaveCount(0)
})
