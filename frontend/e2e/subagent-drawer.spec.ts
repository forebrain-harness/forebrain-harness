import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test, type Locator, type Page } from '@playwright/test'

import { seedSessionInRoot, shot, signIn } from './support'

/**
 * The conversation's subagent drawer, proven on the real page at 1280×800 (the
 * width where the drawer is a column beside the conversation, not an overlay):
 *
 * - a dispatched subagent opens as a tab beside the conversation, its task row
 *   marked as the one in front, and the conversation's own run line sits above
 *   its composer while the turn it started is in flight;
 * - the drawer and the workbench share the right-hand column, one at a time;
 * - a tab's composer bar names the model and the context the spawn fixed,
 *   carries its own "+" (attachments and skills, no goal), and its own @ file
 *   picker, and the tab's working line sits above it while the subagent runs;
 * - closing a tab is a window shut, not a stop, and the drawer's shape comes
 *   back after a reload.
 */

// Where the drawer is a column and the main composer has to keep fitting.
test.use({ viewport: { width: 1280, height: 800 } })

/** A one-pixel PNG, valid enough for the gateway to store and serve. */
const PNG =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='

/** The gateway's state database, by name under E2E_HOME. */
function stateDb(): string {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const found = execFileSync('find', [home, '-name', 'forebrain.state.sqlite'], { encoding: 'utf8' })
    .split('\n').map((line) => line.trim()).filter(Boolean)
  if (!found[0]) throw new Error('no state database found under E2E_HOME')
  return found[0]
}

/** One sqlite3 statement batch against the gateway's own state database. */
function sqlite(sql: string): void {
  execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(), sql], { stdio: ['ignore', 'pipe', 'pipe'] })
}

/** SQL quoting for a raw JSON document stored as the column's text. */
function sqlJson(value: unknown): string {
  return JSON.stringify(value).replace(/'/g, "''")
}

/** One session event, at a moment given in epoch ms. */
function event(session: string, runId: string, eventId: string, type: string, payload: unknown, atMs: number): string {
  return `INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
    VALUES('${session}', '${runId}', '${eventId}', '${type}', '${sqlJson(payload)}', ${atMs});`
}

/** The id the seeded subagent keeps in the roster, the drawer's tabs and the
 *  conversation's card alike. */
const AGENT_ID = 'subagent-e2e-live'

/** The run id the seeded subagent's events belong to; one per conversation, so
 *  two cases in this file never collide on the runs table's primary key. */
function seededRunId(session: string): string {
  return `run-e2e-drawer-${session}`
}

/**
 * One subagent of this conversation, still running, with the model and the
 * context budget its spawn resolved — the same events a real dispatch writes,
 * replayed through the page's ordinary history path. Seeding is how a
 * deterministically *running* subagent exists: no model, fake or real, keeps
 * one alive on demand.
 */
function seedRunningSubagent(session: string): void {
  const now = Math.floor(Date.now() / 1000)
  const runId = seededRunId(session)
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('${runId}', '${session}', 'dispatch the probe', 'done', ${now - 90}, ${now - 90});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', '${runId}', 'user', 'dispatch the probe', ${now - 90});
    ${event(session, runId, 'e2e-drawer-spawn', 'subagent_spawned', {
      agent_id: AGENT_ID, agent_type: 'explore', task_id: AGENT_ID,
      title: 'drawer probe', task: 'read the README and answer briefly',
      execution_id: 'exec-e2e-drawer',
      model_provider: 'zhipuai', model: 'glm-5.3-flash', reasoning_effort: 'high',
    }, (now - 30) * 1000)}
    ${event(session, runId, 'e2e-drawer-budget', 'token_budget_updated', {
      agent_id: AGENT_ID, percent_left: 35, context_window: 1_000_000, token_usage: 650_000,
    }, (now - 29) * 1000)}
  `)
}

/** End that subagent's execution, the way its own run would. */
function endRunningSubagent(session: string): void {
  const now = Math.floor(Date.now() / 1000)
  sqlite(`
    ${event(session, seededRunId(session), 'e2e-drawer-end', 'subagent_ended', {
      agent_id: AGENT_ID, agent_type: 'explore', task_id: AGENT_ID,
      status: 'ok', execution_id: 'exec-e2e-drawer', finished_at_ms: now * 1000,
    }, now * 1000)}
  `)
}

/** The gap between the bottom of one row and the top of the next, in px. */
async function verticalGap(above: Locator, below: Locator): Promise<number> {
  const a = await above.boundingBox()
  const b = await below.boundingBox()
  if (!a || !b) throw new Error('a measured control was not laid out')
  return Math.round(b.y - (a.y + a.height))
}

/** The browser storage key the conversation's drawer shape is kept under. */
function browseKey(session: string): string {
  return `forebrain:chat-browse:v1:${session}`
}

/** A fresh scratch directory the gateway can bind a project to, holding one
 *  file the @ picker has something to offer. */
function scratchRoot(name: string): string {
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const root = join(parent, 'drawer-attach', `${name}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`)
  mkdirSync(root, { recursive: true })
  writeFileSync(join(root, 'README.md'), '# drawer e2e\n')
  return root
}

test('a dispatched subagent opens beside the conversation, not in place of it', async ({ page }) => {
  test.info().setTimeout(240_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-subagent')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')

  // The conversation's own state line, where the terminal draws it: above the
  // composer, gone the moment the turn ends. Everything read from it is read
  // and photographed while the turn is short-lived but real, before moving on.
  const runLine = page.locator('[data-testid="run-status-line"]')
  const mainInput = page.locator('.chat-shell main [data-slot="input-group"]')
  const startedAt = Date.now()
  await page.fill('textarea', 'delegate the probe [[e2e:subagent-net]]')
  await page.press('textarea', 'Enter')
  await expect(runLine).toBeVisible({ timeout: 60_000 })
  const runText = await runLine.innerText()
  const runBox = await runLine.boundingBox()
  const inputBox = await mainInput.boundingBox()
  const seenAfter = Date.now() - startedAt
  expect(runText.startsWith('Working (')).toBe(true)
  expect(runText).toContain('esc to interrupt')
  expect(runBox).not.toBeNull()
  expect(inputBox).not.toBeNull()
  // Directly above the composer: it ends where the input group begins.
  const runGap = Math.round((inputBox?.y ?? 0) - ((runBox?.y ?? 0) + (runBox?.height ?? 0)))
  expect(runGap).toBeGreaterThanOrEqual(-1)
  expect(runGap).toBeLessThan(16)
  await shot(page, '078-main-run-line')

  // The dispatch is this conversation's: the top bar gains the drawer's own
  // control, and the workbench's control carries no count.
  await page.waitForSelector('[data-testid="subagent-call-card"]', { timeout: 90_000 })
  const toggle = page.locator('[data-testid="subagent-drawer-toggle"]')
  await expect(toggle).toBeVisible()
  await expect(page.locator('[data-testid="workbench-toggle"] .forebrain-chip-count')).toHaveCount(0)

  // The card's task row is the way in, and the row says it is the one in front.
  const row = page.locator('[data-testid="subagent-task-row"]').first()
  await row.click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()
  await expect(drawer.locator('[role="tab"][aria-selected="true"]')).toBeVisible()
  await expect(row).toHaveAttribute('aria-current', 'true')
  await shot(page, '078-drawer-live')

  // The main composer still fits with the drawer open at 1280: nothing in it
  // overflows, its send button is on screen, and the approval chip names its
  // preset alone.
  expect(await mainInput.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true)
  const send = await page.locator('.chat-shell main button[aria-label="Send"]').boundingBox()
  expect(send).not.toBeNull()
  expect(Math.round((send?.x ?? 0) + (send?.width ?? 0))).toBeLessThanOrEqual(1280)
  // The approval chip names its preset alone: the preset is picked first so
  // the label under test is a real one rather than whatever this conversation
  // started on.
  const picker = page.locator('[data-testid="approval-picker"]')
  await page.click('[data-testid="approval-picker"]')
  await page.click('[data-testid="approval-option-auto"]')
  await expect(picker).toHaveText('Default')
  await expect(picker).not.toContainText('Approvals:')

  // The right-hand column holds one of the two at a time, and the open tab
  // survives the switch.
  const workbench = page.locator('[data-testid="chat-workbench"]')
  await page.locator('[data-testid="workbench-toggle"]').click()
  await expect(drawer).toHaveCount(0)
  await expect(workbench).toBeVisible()
  await toggle.click()
  await expect(drawer).toBeVisible()
  await expect(workbench).toHaveCount(0)
  await expect(drawer.locator('[role="tab"][aria-selected="true"]')).toBeVisible()

  // The turn is over: the line is gone with it.
  await expect(runLine).toHaveCount(0, { timeout: 120_000 })
  test.info().annotations.push({
    type: 'run line',
    description: `visible ${seenAfter}ms after Enter (before the drawer work), gone after the turn`,
  })
})

test("a running subagent's own tab carries its working line and its context pill", async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-subagent')
  seedRunningSubagent(session)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="subagent-call-card"]')).toBeVisible({ timeout: 30_000 })
  await page.locator('[data-testid="subagent-drawer-toggle"]').click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()

  // The terminal's working line, above the tab's composer rather than in the
  // record: the state and its clock, never inside the transcript.
  const working = page.locator('[data-testid="subagent-working"]')
  await expect(working).toBeVisible()
  await expect(working).toContainText('Working (')
  await expect(drawer.locator('[role="log"]')).not.toContainText('Working (')
  const drawerInput = drawer.locator('[data-slot="input-group"]')
  const workingGap = await verticalGap(working, drawerInput)
  expect(workingGap).toBeGreaterThanOrEqual(-1)
  expect(workingGap).toBeLessThan(16)

  // The composer bar names what the spawn fixed: the percentage now, the whole
  // reading in the card above it, and no control inside that card.
  const pill = drawer.locator('[data-testid="subagent-run-pill"]')
  await expect(pill).toContainText('glm-5.3-flash')
  await expect(drawer.locator('[data-testid="subagent-budget"]')).toHaveText('65%')
  await pill.click()
  const card = page.locator('[data-testid="subagent-run-card"]')
  await expect(card).toBeVisible()
  await expect(card).toContainText('65%')
  await expect(card).toContainText('650k / 1M')
  await expect(card).toContainText('zhipuai/glm-5.3-flash')
  await expect(card.locator('[data-testid="subagent-context-window"]')).toHaveText('1M')
  await expect(card.locator('button, a')).toHaveCount(0)
  // Every label occupies exactly one line beside its value: the prototype's
  // 76px label column is what its Chinese labels measure, and an English one
  // that is longer ("Context window") widens its own column instead of folding.
  const labelLines = await card.locator('dt').evaluateAll((els) => els.map((el) => {
    const range = document.createRange()
    range.selectNodeContents(el)
    return range.getClientRects().length
  }))
  expect(labelLines.length).toBeGreaterThan(0)
  for (const lines of labelLines) expect(lines).toBe(1)
  await shot(page, '078-drawer-run-card')

  // The subagent ends: its working line leaves with it, while the drawer and
  // the tab come back as they were (the persisted shape).
  await page.keyboard.press('Escape')
  await expect(card).toHaveCount(0)
  endRunningSubagent(session)
  await expect.poll(async () => page.evaluate((key) => {
    const raw = localStorage.getItem(key)
    if (!raw) return false
    const parsed = JSON.parse(raw) as { drawer?: { open?: boolean } }
    return parsed.drawer?.open === true
  }, browseKey(session))).toBe(true)
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="subagent-drawer"]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator(`[data-testid="subagent-tab-${AGENT_ID}"]`)).toBeVisible()
  await expect(page.locator('[data-testid="subagent-working"]')).toHaveCount(0)
})

test("a tab's + is the composer's own, and its sheet is a menu off the button", async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-subagent')
  seedRunningSubagent(session)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="subagent-call-card"]')).toBeVisible({ timeout: 30_000 })
  await page.locator('[data-testid="subagent-drawer-toggle"]').click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()

  // The tab's "+" offers attachments and the skills — and no goal, which
  // belongs to the whole conversation rather than to one subagent.
  await drawer.locator('[data-testid="composer-plus"]').click()
  await expect(drawer.locator('[data-testid="plus-attach"]')).toBeVisible()
  await expect(drawer.locator('[data-testid="plus-goal"]')).toHaveCount(0)
  const drawerComposer = await drawer.locator('[data-slot="input-group"]').boundingBox()
  const drawerSheet = await drawer.locator('.forebrain-plus-pop').boundingBox()
  await shot(page, '078-drawer-plus')

  // The conversation's own "+" opens the same sheet from the same component,
  // so its measurements are recorded beside the drawer's.
  await page.locator('.chat-shell main [data-testid="composer-plus"]').click()
  const mainComposer = await page.locator('.chat-shell main [data-slot="input-group"]').boundingBox()
  const mainSheet = await page.locator('.chat-shell main .forebrain-plus-pop').boundingBox()
  test.info().annotations.push({
    type: 'plus sheet width',
    description: `drawer sheet ${drawerSheet?.width} vs drawer composer ${drawerComposer?.width};`
      + ` conversation sheet ${mainSheet?.width} vs conversation composer ${mainComposer?.width}`,
  })

  // The sheet is a menu that hangs off the "+" itself (the 2026-10-23
  // redesign): its own width — never the composer's, never past the column —
  // its left edge on the button's, opening above the input area.
  const drawerPlusBox = await drawer.locator('[data-testid="composer-plus"]').boundingBox()
  expect(drawerSheet).not.toBeNull()
  expect(drawerComposer).not.toBeNull()
  expect((drawerSheet?.width ?? 0)).toBeGreaterThanOrEqual(260)
  expect((drawerSheet?.width ?? 0)).toBeLessThanOrEqual(440)
  expect((drawerSheet?.width ?? 0)).toBeLessThanOrEqual(drawerComposer?.width ?? 0)
  expect(Math.abs((drawerSheet?.x ?? 0) - (drawerPlusBox?.x ?? 0))).toBeLessThanOrEqual(2)
  expect((drawerSheet?.x ?? 0)).toBeGreaterThanOrEqual((drawerComposer?.x ?? 0) - 2)
  expect((drawerSheet?.x ?? 0) + (drawerSheet?.width ?? 0))
    .toBeLessThanOrEqual((drawerComposer?.x ?? 0) + (drawerComposer?.width ?? 0) + 2)
  // It opens above the "+" — over the composer's own input area, which the
  // button's row sits inside — never below the button that opened it.
  expect((drawerSheet?.y ?? 0) + (drawerSheet?.height ?? 0)).toBeLessThanOrEqual(drawerPlusBox?.y ?? 0)
  // The conversation's own "+" opens the same sheet from the same component:
  // the same menu width, anchored the same way.
  const mainPlusBox = await page.locator('.chat-shell main [data-testid="composer-plus"]').boundingBox()
  expect((mainSheet?.width ?? 0)).toBeGreaterThanOrEqual(260)
  expect((mainSheet?.width ?? 0)).toBeLessThanOrEqual(440)
  expect((mainSheet?.width ?? 0)).toBeLessThanOrEqual(mainComposer?.width ?? 0)
  expect(Math.abs((mainSheet?.x ?? 0) - (mainPlusBox?.x ?? 0))).toBeLessThanOrEqual(2)
  expect((mainSheet?.y ?? 0) + (mainSheet?.height ?? 0)).toBeLessThanOrEqual(mainPlusBox?.y ?? 0)
})

test("a tab's own composer adds attachments and @ references, and closing it is not a stop", async ({ page }) => {
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  // Files the @ picker can offer, saved into this conversation's project.
  const root = scratchRoot('live')
  const session = await seedSessionInRoot(page, root, 'e2e-subagent')
  seedRunningSubagent(session)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const cardInConversation = page.locator('[data-testid="subagent-call-card"]')
  await expect(cardInConversation).toBeVisible({ timeout: 30_000 })
  await expect(cardInConversation).toContainText('Running 1 explore task')
  await page.locator('[data-testid="subagent-drawer-toggle"]').click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()

  // An attachment picked from the "+" is saved through the same "save to"
  // dialog the conversation's composer uses, and lands in THIS tab's composer.
  await drawer.locator('[data-testid="composer-plus"]').click()
  await expect(drawer.locator('[data-testid="plus-attach"]')).toBeVisible()
  await drawer.locator('[data-testid="plus-attach"]').click()
  await page.setInputFiles('[data-testid="subagent-drawer"] input[type=file]', {
    name: 'probe.png',
    mimeType: 'image/png',
    buffer: Buffer.from(PNG, 'base64'),
  })
  await page.click('[data-testid="attach-confirm"]')
  await expect(drawer).toContainText('probe.png')
  await expect(page.locator('.chat-shell main')).not.toContainText('probe.png')

  // @ offers this conversation's project files, from the same picker the
  // conversation's composer opens, and writes the reference into the tab.
  const tabInput = drawer.locator('textarea')
  await tabInput.fill('@')
  await expect(drawer.getByRole('menu', { name: 'Mention menu' })).toBeVisible()
  await drawer.locator('[role="menuitem"]', { hasText: 'File' }).click()
  const firstFile = drawer.locator('[data-file-option-index="0"]')
  await expect(firstFile).toBeVisible({ timeout: 30_000 })
  await shot(page, '078-drawer-mention-candidates')
  await firstFile.click()
  // The reference is written the way the terminal's own composer writes it:
  // the picked file's path, decided by the shared engine rather than here.
  await expect(tabInput).toHaveValue(/README\.md/)
  await expect(drawer.getByRole('menu', { name: 'Mention menu' })).toHaveCount(0)
  await shot(page, '078-drawer-mention')

  // Closing the only tab puts the drawer away; the subagent is untouched.
  await drawer.locator(`[data-testid="subagent-tab-close-${AGENT_ID}"]`).click()
  await expect(page.locator('[data-testid="subagent-drawer"]')).toHaveCount(0)
  await expect(cardInConversation).toContainText('Running 1 explore task')
  await expect(cardInConversation).not.toContainText('Cancelled')

  // The top bar reopens it, and the "+" menu names this subagent as open.
  await page.locator('[data-testid="subagent-drawer-toggle"]').click()
  await expect(page.locator(`[data-testid="subagent-tab-${AGENT_ID}"]`)).toBeVisible()
  await page.locator('[data-testid="subagent-drawer-add"]').click()
  const menuRow = page.locator(`[data-testid="subagent-menu-${AGENT_ID}"]`)
  await expect(menuRow).toBeVisible()
  await expect(menuRow.locator('.subagent-drawer-menu-side')).toHaveText('Open')

  // The drawer's shape is the conversation's own, kept across a reload.
  await expect.poll(async () => page.evaluate((key) => {
    const raw = localStorage.getItem(key)
    if (!raw) return []
    const parsed = JSON.parse(raw) as { drawer?: { open?: boolean; tabs?: string[] } }
    return parsed.drawer?.open === true ? (parsed.drawer?.tabs ?? []) : []
  }, browseKey(session))).toEqual([AGENT_ID])
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="subagent-drawer"]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator(`[data-testid="subagent-tab-${AGENT_ID}"]`)).toBeVisible()
})

/** The drawer's width as drawn, in whole pixels. */
async function drawerWidth(page: Page): Promise<number> {
  const box = await page.locator('[data-testid="subagent-drawer"]').boundingBox()
  return Math.round(box?.width ?? 0)
}

/** Drag the drawer's left edge by `dx` px — a negative `dx` widens it. */
async function dragEdge(page: Page, dx: number): Promise<void> {
  const handle = page.locator('[data-testid="subagent-drawer-resizer"]')
  const box = await handle.boundingBox()
  if (!box) throw new Error('the drawer handle was not laid out')
  const x = box.x + box.width / 2
  const y = box.y + box.height / 2
  await page.mouse.move(x, y)
  await page.mouse.down()
  await page.mouse.move(x + dx, y, { steps: 8 })
  await page.mouse.up()
}

test('the drawer is resized by its left edge, for this session only', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-subagent-drag')
  seedRunningSubagent(session)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.locator('[data-testid="subagent-drawer-toggle"]').click({ timeout: 30_000 })
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()
  // Measure it where it lands, not where the slide-in happens to be.
  await expect(drawer).toHaveCSS('transform', 'none')
  const design = await drawerWidth(page)
  expect(design).toBe(440)

  // 80px of travel left is 80px of drawer, taken from the conversation.
  await expect(page.locator('[data-testid="subagent-drawer-resizer"]')).toBeVisible()
  await dragEdge(page, -80)
  await expect.poll(async () => Math.abs((await drawerWidth(page)) - (design + 80))).toBeLessThanOrEqual(2)
  await shot(page, 'subagent-drawer-resized')

  // A double-click on the handle is the way back to the design width.
  await page.locator('[data-testid="subagent-drawer-resizer"]').dblclick()
  await expect.poll(() => drawerWidth(page)).toBe(design)

  // Dragged wide and reloaded: the width was this tab's, so the drawer comes
  // back at 440 while its own shape (open, tab in front) survives.
  await dragEdge(page, -80)
  await expect.poll(async () => Math.abs((await drawerWidth(page)) - (design + 80))).toBeLessThanOrEqual(2)
  await expect.poll(async () => page.evaluate((key) => {
    const raw = localStorage.getItem(key)
    if (!raw) return false
    const parsed = JSON.parse(raw) as { drawer?: { open?: boolean } }
    return parsed.drawer?.open === true
  }, browseKey(session))).toBe(true)
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="subagent-drawer"]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator(`[data-testid="subagent-tab-${AGENT_ID}"]`)).toBeVisible()
  await expect(page.locator('[data-testid="subagent-drawer"]')).toHaveCSS('transform', 'none')
  expect(await drawerWidth(page)).toBe(design)
})
