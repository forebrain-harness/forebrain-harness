import { expect, test } from '@playwright/test'

import { expectAssistantReply, seedProject, signIn, shot } from './support'

/**
 * Settings · Advanced · Logs (plan 288) on the real page and the real gateway:
 * a turn's records are there, narrowed to the conversation they belong to; one
 * model call opens as a call with its own request, its own answer and its own
 * events; and the DEBUG records a successful read writes are one button away.
 *
 * The records are the gateway's own — this spec drives one turn through the
 * fake provider and then reads what the process wrote to `$FOREBRAIN_HOME/logs`,
 * which is the only data path the page has.
 */
test('the log page shows a turn, its call and its payloads', async ({ page }) => {
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  // 1. One turn that calls a tool, in a conversation with a title of its own, so
  //    the page has a turn, a tool call, two model calls and their payloads.
  const projectId = await seedProject(page, 'logs-e2e')
  const created = await page.request.post('/api/chat/sessions', {
    data: { project_id: projectId, title: 'logs e2e turn' },
  })
  expect(created.ok()).toBeTruthy()
  const session = String(((await created.json()) as { id: string }).id)
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', `[[e2e:tool-call]]${JSON.stringify({ name: 'peer_list', arguments: {} })}\n\n谁在线？`)
  await page.press('textarea', 'Enter')
  const allow = page.locator('[data-testid="pending-decision-accept"]')
  await allow
    .or(page.getByText(process.env.E2E_REPLY_OK ?? 'E2E_REPLY_OK'))
    .first()
    .waitFor({ timeout: 60_000 })
  if (await allow.isVisible()) await allow.click()
  await expectAssistantReply(page)

  // 2. The page itself, in the advanced group.
  await page.goto('/settings?tab=logs', { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="logs-tab"]')).toBeVisible()
  const rows = page.locator('[data-log-row]')
  await expect.poll(async () => rows.count(), { timeout: 30_000 }).toBeGreaterThan(0)
  await shot(page, 'logs-default')

  // 3. One conversation's records, and only those. The empty session cell is the
  //    signal the filtered page has arrived: before it, the gateway's own access
  //    records — which belong to no conversation — are in the table.
  await page.click('[data-testid="logs-session"]')
  await page.click(`[data-testid="logs-session-option-${session}"]`)
  await expect(page.locator('[data-testid="logs-session"]')).toContainText('logs e2e turn')
  await expect.poll(async () => page.locator('[data-testid="logs-row-no-session"]').count(), { timeout: 30_000 }).toBe(0)
  await expect.poll(async () => rows.count(), { timeout: 30_000 }).toBeGreaterThan(0)

  // 4. A model call and a tool call, each by its own type filter. A gateway
  //    session writes the calls, not the turn: the turn records (`turn started`
  //    / `turn completed`) are the terminal's own write points, so the type
  //    menu offers 轮次 and this run has none of its rows.
  await page.selectOption('[data-testid="logs-kind"]', 'llm')
  await expect.poll(async () => (await rows.first().innerText()).includes('model='), { timeout: 30_000 }).toBe(true)
  const call = await rows.first().innerText()
  expect(call).toContain('model=deepseek-v4-flash')
  // The call's own status and duration, each drawn by its own span.
  expect(await rows.first().locator('.st').first().innerText()).toMatch(/^\d{3}$/)
  expect(await rows.first().locator('.dur').first().innerText()).toMatch(/ms$/)

  expect(await page.locator('[data-testid="logs-kind"] option').allInnerTexts()).toContain('轮次')
  await page.selectOption('[data-testid="logs-kind"]', 'tool')
  await expect.poll(async () => (await rows.first().innerText()).includes('peer_list'), { timeout: 30_000 }).toBe(true)

  await page.selectOption('[data-testid="logs-kind"]', '')
  await expect
    .poll(
      async () => {
        const text = await page.locator('[data-testid="logs-table"]').innerText()
        return text.includes('model=') && text.includes('peer_list')
      },
      { timeout: 30_000 },
    )
    .toBe(true)

  // 5. One call opens as a call: its own numbers, its own answer, its own events.
  //    The turn made two calls, and the answer is the later one's; the rows are
  //    read by their own time so a page that re-orders cannot pick the wrong one.
  const before = await rows.count()
  const modelRows = page.locator('[data-log-row]').filter({ hasText: 'model=' })
  const times = await modelRows.locator('.tm').allInnerTexts()
  const latest = times.indexOf([...times].sort().at(-1) ?? '')
  await modelRows.nth(latest).click()
  const drawer = page.locator('[data-testid="log-drawer-title"]')
  await expect(drawer).toContainText('模型调用 · logs e2e turn')
  await expect(page.locator('[data-testid="log-drawer-overview"]')).toBeVisible()
  await page.waitForTimeout(400)
  await shot(page, 'logs-call-overview')

  await page.click('[data-testid="log-drawer-tab-resp"]')
  await expect(page.locator('[data-testid="log-drawer-answer"]')).toContainText(process.env.E2E_REPLY_OK ?? 'E2E_REPLY_OK')
  await shot(page, 'logs-call-response')

  await page.click('[data-testid="log-drawer-tab-raw"]')
  const events = page.locator('[data-testid="log-drawer-raw-rows"] > div')
  await expect.poll(async () => events.count(), { timeout: 30_000 }).toBeGreaterThan(0)
  await shot(page, 'logs-call-raw')

  await page.click('[data-testid="log-drawer-tab-req"]')
  await expect(page.locator('[data-testid="log-drawer-request-head"]')).toBeVisible()
  await expect(page.locator('[data-testid="log-drawer-request-download"]')).toBeVisible()
  await shot(page, 'logs-call-request')

  // 6. Closed with Escape, the table is back exactly as it was — nothing moved.
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-testid="log-drawer-title"]')).toHaveCount(0)
  await expect.poll(async () => rows.count(), { timeout: 30_000 }).toBe(before)

  // 7. 调试 adds the records a quiet run keeps out of the way: the requests the
  //    calls sent, and the gateway's own successful reads.
  await page.click('[data-testid="logs-level-debug"]')
  await expect.poll(async () => rows.count(), { timeout: 30_000 }).toBeGreaterThan(before)
  await shot(page, 'logs-debug')

  // 8. A range with nothing in it says so, and offers the three ways out.
  await page.fill('[data-testid="logs-search"]', 'zzz-no-such-record')
  await expect(page.locator('[data-testid="logs-empty"]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-testid="logs-empty"]')).toContainText('这个范围内没有符合条件的日志')
  await shot(page, 'logs-empty')
})
