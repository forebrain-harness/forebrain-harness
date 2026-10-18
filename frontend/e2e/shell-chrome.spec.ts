import { expect, test, type Page } from '@playwright/test'

import { seedProjectSession, shot, signIn } from './support'

/**
 * The shell's chrome and the conversation page, as the design decides them
 * (plan 072): the top bar is one 48px row whose controls are all 30px with the
 * light/dark switch last; the rail's collapse control sits at the foot, under
 * 设置 once the rail is icons only; the home page is the new-conversation page
 * — centred, unnamed, no workbench — and a conversation's name is the engine's
 * own rule (first line, 50 characters, "..."), capped at 360px in the bar.
 */

// The size the design was drawn at.
test.use({ viewport: { width: 1320, height: 800 } })

async function railTheme(page: Page): Promise<string> {
  return page.evaluate(() => document.documentElement.dataset.railTheme ?? '')
}

async function boxOf(page: Page, selector: string) {
  const box = await page.locator(selector).first().boundingBox()
  expect(box, `${selector} has no box`).not.toBeNull()
  return box!
}

test('the home page is the new conversation: centred, unnamed, no workbench', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })

  await expect(page.locator('[data-testid="chat-greeting"]')).toBeVisible()
  await expect(page.locator('[data-testid="chat-starters"]')).toBeVisible()
  // A new chat names nothing yet, and its third column waits for a message.
  await expect(page.locator('[data-testid="chat-session-title"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="workbench-toggle"]')).toHaveCount(0)

  // Greeting, composer and quick starts are one centred group: the pane's top
  // and bottom insets match (prototype .nc).
  const pane = await boxOf(page, '.chat-shell main')
  const greeting = await boxOf(page, '[data-testid="chat-greeting"]')
  const starters = await boxOf(page, '[data-testid="chat-starters"]')
  const above = greeting.y - pane.y
  const below = pane.y + pane.height - (starters.y + starters.height)
  expect(Math.abs(above - below)).toBeLessThanOrEqual(8)
  await shot(page, '072-1-fresh-chat')

  // The project chooser opens downwards from the pill and stays on screen.
  await page.click('[data-testid="project-pill"]')
  await expect(page.locator('.pj-pop')).toBeVisible()
  const popover = await boxOf(page, '.pj-pop')
  const viewport = page.viewportSize()!
  expect(popover.y).toBeGreaterThanOrEqual(0)
  expect(popover.y + popover.height).toBeLessThanOrEqual(viewport.height)
  expect(popover.x).toBeGreaterThanOrEqual(0)
  expect(popover.x + popover.width).toBeLessThanOrEqual(viewport.width)
  // The chooser's own search field holds the keyboard: Escape closes it.
  await page.keyboard.press('Escape')
  await expect(page.locator('.pj-pop')).toHaveCount(0)
})

test('the top bar ends with new agent, language and the theme switch at 30px', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })

  const bar = await boxOf(page, '.forebrain-topbar')
  expect(Math.abs(bar.height - 48)).toBeLessThanOrEqual(1)

  const newAgent = await boxOf(page, '[data-testid="new-agent"]')
  const language = await boxOf(page, '.forebrain-language-trigger')
  const theme = await boxOf(page, '[data-testid="theme-toggle"]')
  // Left to right, in that order, none of them overlapping.
  expect(theme.x).toBeGreaterThan(language.x + language.width - 1)
  expect(language.x).toBeGreaterThan(newAgent.x + newAgent.width - 1)
  for (const control of [newAgent, language, theme]) expect(Math.abs(control.height - 30)).toBeLessThanOrEqual(1)
  expect(Math.abs(theme.width - 30)).toBeLessThanOrEqual(1)

  // The switch flips the shell's rail theme, and names what it will do below
  // itself on hover.
  const before = await railTheme(page)
  await expect(page.locator('[data-testid="theme-toggle"]')).toHaveAttribute('aria-label', /菜单栏切换为|Switch the rail to/)
  await page.hover('[data-testid="theme-toggle"]')
  const tip = page.locator('[role="tooltip"]')
  await expect(tip).toBeVisible()
  const tipBox = await boxOf(page, '[role="tooltip"]')
  expect(tipBox.y).toBeGreaterThanOrEqual(theme.y + theme.height - 1)
  await shot(page, '072-2-theme-tip')
  await page.click('[data-testid="theme-toggle"]')
  await expect.poll(() => railTheme(page)).not.toBe(before)
  // A manual pick is the user's; put it back so later specs see the default.
  await page.click('[data-testid="theme-toggle"]')
  await expect.poll(() => railTheme(page)).toBe(before)
})

test('the rail collapses from the foot, under 设置', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })

  const rail = await boxOf(page, '.forebrain-rail')
  expect(Math.abs(rail.width - 264)).toBeLessThanOrEqual(1)
  const settings = await boxOf(page, '.forebrain-rail-foot .forebrain-rail-link')
  const collapse = await boxOf(page, '[data-testid="rail-collapse"]')
  // One row while expanded: the control sits right of 设置.
  expect(collapse.x).toBeGreaterThan(settings.x + settings.width - 1)

  await page.click('[data-testid="rail-collapse"]')
  await expect.poll(async () => Math.abs((await boxOf(page, '.forebrain-rail')).width - 64) <= 1).toBe(true)
  const collapsedSettings = await boxOf(page, '.forebrain-rail-foot .forebrain-rail-link')
  const collapsedButton = await boxOf(page, '[data-testid="rail-collapse"]')
  // Icons only: the control sits under 设置.
  expect(collapsedButton.y).toBeGreaterThan(collapsedSettings.y + collapsedSettings.height - 1)
  await shot(page, '072-3-rail-collapsed')

  await page.click('[data-testid="rail-collapse"]')
  await expect.poll(async () => Math.abs((await boxOf(page, '.forebrain-rail')).width - 264) <= 1).toBe(true)
})

test('the first message names the conversation and brings the workbench back', async ({ page }) => {
  test.info().setTimeout(150_000)
  await signIn(page)
  // A conversation in a project, so the new chat preselects that project.
  const seeded = await seedProjectSession(page, 'shell-chrome')
  await page.goto(`/?session=${seeded}`, { waitUntil: 'networkidle' })
  await page.goto('/', { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="project-pill"]')).not.toHaveText(/选择项目|Pick a project/)

  const firstLine = 'l'.repeat(60)
  await page.fill('textarea', `${firstLine}\nand a second line the title never shows`)
  await page.press('textarea', 'Enter')

  // The engine's rule, already in the bar: the first line's 50 characters
  // then "..." (pkg/state generateSessionTitle).
  await expect(page.locator('[data-testid="chat-session-title"]')).toHaveText(`${'l'.repeat(50)}...`, { timeout: 30_000 })
  // The conversation exists now, so the third column has its toggle back —
  // closed, as it starts.
  await expect(page.locator('[data-testid="workbench-toggle"]')).toBeVisible()
  await expect(page.locator('[data-testid="workbench-toggle"]')).toHaveAttribute('aria-pressed', 'false')
  await expect(page.locator('[data-testid="chat-workbench"]')).toHaveCount(0)

  // 新聊天 in the drawer opens the new-chat page: the conversation leaves the
  // screen with its title, its thread and its third column.
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await page.locator('[data-testid="drawer-new-chat"]').click()
  await expect(page.locator('[data-testid="chat-greeting"]')).toBeVisible()
  await expect(page.locator('[data-testid="chat-session-title"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="workbench-toggle"]')).toHaveCount(0)
  // The thread went with it: the message that named it is not on screen.
  await expect(page.locator('.chat-scrollbar')).toHaveCount(0)
})

test('a long title is cut at 360px, names itself on hover and leaves the controls alone', async ({ page }) => {
  await signIn(page)
  const session = await seedProjectSession(page, 'shell-chrome-long')
  const long = 'Resolve-the-flaky-gateway-tests-before-the-release-cut'.repeat(2)
  const titled = await page.request.post(`/api/chat/sessions/${session}/title`, { data: { title: long } })
  expect(titled.ok(), await titled.text()).toBeTruthy()

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const title = page.locator('[data-testid="chat-session-title"]')
  await expect(title).toHaveText(long, { timeout: 15_000 })
  const titleBox = await boxOf(page, '[data-testid="chat-session-title"]')
  expect(titleBox.width).toBeLessThanOrEqual(360)
  expect(await title.evaluate((node) => node.scrollWidth > node.clientWidth)).toBe(true)

  // Every control stays in the viewport and keeps its own room: the title is
  // what gave way, not them.
  const viewport = page.viewportSize()!
  const controls = ['[data-testid="new-agent"]', '.forebrain-language-trigger', '[data-testid="theme-toggle"]']
  let right = 0
  for (const selector of controls) {
    const box = await boxOf(page, selector)
    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width)
    expect(box.x).toBeGreaterThan(right)
    right = box.x + box.width - 1
  }

  // The cut title names itself in full on hover.
  await title.hover()
  const tip = page.locator('[role="tooltip"]')
  await expect(tip).toHaveText(long)
  await shot(page, '072-4-long-title')
})

test('the workshop top bar has no back arrow and names the skill once', async ({ page }) => {
  await signIn(page)
  await page.goto('/workshop?mode=create&scope=agent', { waitUntil: 'networkidle' })

  await expect(page.locator('[data-testid="workshop-back"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="workshop-model"]')).toHaveCount(0)
  // The breadcrumb's own 技能 is the way back.
  await page.click('[data-testid="workshop-crumb-skills"]')
  await page.waitForURL((url) => url.pathname === '/skills')
})
