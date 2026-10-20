import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

import { seedProjectSession, signIn, shot } from './support'

/**
 * The conversation's message cards, proven on the real page: a call's header
 * stays one line however long the command is (the command itself moves into
 * the body), the todo card says 更新待办 in a Chinese interface, the agent's
 * questions are asked one screen at a time behind tabs, and the reading mode
 * in the composer hides the run's cards without hiding the answers.
 *
 * The rows and events are seeded straight into the gateway's state database —
 * the same shapes a real turn persists — because the fake provider answers
 * every prompt with one fixed sentence and never issues a tool call. The page
 * then renders them through the same reload path a real session takes.
 */

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

/** A stored assistant row that issued the given calls. */
function assistantRow(session: string, runId: string, calls: { id: string; name: string; args: unknown }[], atSec: number): string {
  const parts = [{
    type: 'tool_calls',
    tool_calls: calls.map((call) => ({
      id: call.id,
      type: 'function',
      function: { name: call.name, arguments: JSON.stringify(call.args) },
    })),
  }]
  return `INSERT INTO fb_messages(session_id, run_id, role, content, parts, created_at)
    VALUES('${session}', '${runId}', 'assistant', '', '${sqlJson(parts)}', ${atSec});`
}

/** A stored assistant row carrying the words the model said. */
function saidRow(session: string, runId: string, text: string, atSec: number): string {
  return `INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
    VALUES('${session}', '${runId}', 'assistant', '${text.replace(/'/g, "''")}', ${atSec});`
}

/**
 * A stored tool row answering one call: the completion card's own summary and
 * body (what the surface showed), and the facts the card reads.
 */
function toolRow(
  session: string,
  runId: string,
  callId: string,
  toolName: string,
  summary: string,
  body: string,
  atSec: number,
): string {
  const meta = { tool_name: toolName, status: 'completed' }
  const parts = [
    { tool_call_id: callId, type: 'tool_result_meta' },
    { type: 'tool_display', summary, body, tool_meta_json: JSON.stringify(meta) },
  ]
  return `INSERT INTO fb_messages(session_id, run_id, role, content, parts, tool_step_id, tool_meta_json, created_at)
    VALUES('${session}', '${runId}', 'tool', '${sqlJson({ ok: true })}', '${sqlJson(parts)}', '${callId}',
      '${sqlJson(meta)}', ${atSec});`
}

/** One session event, at a moment given in epoch ms. */
function event(session: string, runId: string, eventId: string, type: string, payload: unknown, atMs: number): string {
  return `INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
    VALUES('${session}', '${runId}', '${eventId}', '${type}', '${sqlJson(payload)}', ${atMs});`
}

/** The tool cards' headers, in the order the transcript drew them. */
const headers = (page: import('@playwright/test').Page) =>
  page.locator('.chat-shell main [data-slot="collapsible-trigger"]')

test('a long call keeps a one-line header and carries the whole command in its body', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'message-cards-head')
  const now = Math.floor(Date.now() / 1000)
  // The engine's summary for a shell call is `ran <the whole command>`, which
  // is exactly what used to stretch the header into a paragraph.
  const command = 'go vet -tags fts5 $(go list ./pkg/... | grep -v third_party) && go test -tags fts5 -count=1 -race ./pkg/tool ./pkg/tui ./pkg/safety ./pkg/gateway'
  const longSummary = `ran ${command}`
  const shortSummary = 'ran ls'
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-cards-head', '${session}', 'check the tree', 'done', ${now - 60}, ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', 'run-e2e-cards-head', 'user', 'check the tree', ${now - 60});
    ${saidRow(session, 'run-e2e-cards-head', 'the tree is clean', now - 50)}
    ${assistantRow(session, 'run-e2e-cards-head', [
      { id: 'call-long', name: 'shell', args: { command } },
      { id: 'call-short', name: 'shell', args: { command: 'ls' } },
    ], now - 45)}
    ${toolRow(session, 'run-e2e-cards-head', 'call-long', 'shell', longSummary, 'ok', now - 40)}
    ${toolRow(session, 'run-e2e-cards-head', 'call-short', 'shell', shortSummary, 'README.md', now - 39)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(headers(page)).toHaveCount(2)

  // The two headers are exactly as tall as each other: the long call is not
  // allowed to stretch the card head, whatever its command says.
  const longBox = await headers(page).nth(0).boundingBox()
  const shortBox = await headers(page).nth(1).boundingBox()
  expect(longBox?.height).toBe(shortBox?.height)

  // The truncated header still holds the whole call, for the hover tooltip and
  // for assistive tech.
  const longTitle = headers(page).nth(0).locator('span[title]').first()
  await expect(longTitle).toHaveAttribute('title', longSummary)
  await expect(longTitle).toHaveText(longSummary)

  // Opening the card puts the command itself at the top of the body, verbatim.
  await headers(page).nth(0).click()
  const full = page.locator('.cmd-full')
  await expect(full).toHaveCount(1)
  await expect(full).toHaveText(longSummary)
  // A short summary is not repeated under itself: one card, one header line.
  await headers(page).nth(1).click()
  await expect(page.locator('.cmd-full')).toHaveCount(1)
  await shot(page, 'message-cards-tool-head')
})

test('the todo card says 更新待办 in a Chinese interface', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'message-cards-todo')
  const now = Math.floor(Date.now() / 1000)
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-cards-todo', '${session}', 'read the README', 'done', ${now - 60}, ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', 'run-e2e-cards-todo', 'user', 'read the README', ${now - 60});
    ${assistantRow(session, 'run-e2e-cards-todo', [
      { id: 'call-todo', name: 'session_todo', args: { action: 'update' } },
    ], now - 50)}
    ${toolRow(session, 'run-e2e-cards-todo', 'call-todo', 'session_todo', 'updated 1 todo', 'Updated Plan', now - 45)}
    ${event(session, 'run-e2e-cards-todo', 'e2e-cards-plan-updated', 'plan_updated', {
      title: 'Updated Plan',
      explanation: '读 README',
      completed: 0,
      total: 1,
      items: [{ id: '1', content: '读 README', status: 'in_progress', active: '读 README' }],
    }, (now - 44) * 1000)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const main = page.locator('.chat-shell main')
  // The engine names the card "Updated Plan"; a Chinese interface says 更新待办.
  await expect(main).toContainText('更新待办')
  await expect(main).toContainText('读 README')
  expect(await main.innerText()).not.toContain('Updated Plan')
  await shot(page, 'message-cards-todo')
})

test('the agent’s questions come one screen at a time, behind tabs', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'message-cards-ask')
  const now = Math.floor(Date.now() / 1000)
  const actionId = 'act-e2e-cards-ask'
  // Two questions, the second one multi-select — the shape the terminal's
  // overlay draws tabs, arrows and a Submit chip for.
  sqlite(`
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
      VALUES('${actionId}', '${session}', 'user_interaction', 'pending', '${sqlJson({
        questions: [
          {
            id: '交付方式',
            header: '交付方式',
            prompt: '这次改动怎么交付？',
            options: [
              { id: 'design', label: '设计方案并开发实施', description: '先写设计再动手' },
              { id: 'direct', label: '直接改' },
            ],
            allow_multiple: false,
            allow_other: true,
          },
          {
            id: '修改范围',
            header: '修改范围',
            prompt: '要动哪些范围？',
            options: [
              { id: 'tui', label: 'TUI' },
              { id: 'web', label: 'Web' },
            ],
            allow_multiple: true,
            allow_other: false,
          },
        ],
      })}', ${now - 60}, ${now - 60});
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const card = page.locator('[data-testid="pending-question"]')
  await expect(card).toBeVisible()

  // One chip per question, all unanswered, plus the Submit chip and the arrows
  // — the terminal's bar, drawn over one question at a time.
  const tab = (i: number) => card.locator(`[data-testid="ask-tab-${i}"]`)
  await expect(tab(0)).toContainText('☐ 交付方式')
  await expect(tab(0)).toHaveAttribute('data-answered', 'false')
  await expect(tab(1)).toContainText('☐ 修改范围')
  await expect(card.locator('[data-testid="ask-tab-submit"]')).toBeVisible()
  // Only the first question is on screen: the second one is behind its chip.
  await expect(card).toContainText('这次改动怎么交付？')
  expect(await card.innerText()).not.toContain('要动哪些范围？')
  await shot(page, 'message-cards-ask-tabs')

  // Answering the first question ticks its chip and moves on, the way the
  // overlay's Enter does.
  await card.locator('[data-testid="ask-option"]').first().click()
  await expect(tab(0)).toHaveAttribute('data-answered', 'true')
  await expect(card).toContainText('要动哪些范围？')

  // The multi-select question only toggles, so the answers are reviewed before
  // they are sent — what was picked, then the one decision that sends it.
  await card.locator('[data-testid="ask-option"]').first().click()
  await card.locator('[data-testid="ask-tab-submit"]').click()
  await expect(card.locator('[data-testid="ask-review"]')).toBeVisible()
  await expect(card.locator('[data-testid="ask-review"]')).toContainText('设计方案并开发实施')
  await expect(card.locator('[data-testid="ask-review"]')).toContainText('TUI')

  const answer = page.waitForRequest((req) => req.url().includes(`/api/actions/${actionId}/answer`) && req.method() === 'POST')
  await card.locator('[data-testid="ask-submit"]').click()
  // The payload is the protocol every surface already spoke: one entry per
  // question, its id and the options picked.
  expect((await (await answer).postDataJSON())?.answers).toEqual([
    { question_id: '交付方式', option_ids: ['design'] },
    { question_id: '修改范围', option_ids: ['tui'] },
  ])
  await expect(page.locator('[data-testid="pending-question"]')).toHaveCount(0, { timeout: 30_000 })
  await shot(page, 'message-cards-ask-answered')
})

test('an answered question card keeps the question and the answer, and its head says only that the user was asked', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'message-cards-ask-done')
  const now = Math.floor(Date.now() / 1000)
  // What the engine prints for a finished question card: the question in its
  // own words, then the answer — never the question's header, which is only a
  // lookup key.
  const body = '这次改动怎么交付？\n  → 设计方案并开发实施\n要动哪些范围？\n  → TUI'
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-cards-ask-done', '${session}', 'ask me', 'done', ${now - 60}, ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', 'run-e2e-cards-ask-done', 'user', 'ask me', ${now - 60});
    ${assistantRow(session, 'run-e2e-cards-ask-done', [
      { id: 'call-ask', name: 'user_interaction', args: { questions: [] } },
    ], now - 50)}
    ${toolRow(session, 'run-e2e-cards-ask-done', 'call-ask', 'user_interaction', 'asked user', body, now - 45)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(headers(page)).toHaveCount(1)
  // The head carries the verb alone — the questions are the body's business.
  await expect(headers(page).first()).toContainText(/asked user/i)
  expect(await headers(page).first().innerText()).not.toContain('交付方式')

  await headers(page).first().click()
  const main = page.locator('.chat-shell main')
  await expect(main).toContainText('这次改动怎么交付？')
  await expect(main).toContainText('→ 设计方案并开发实施')
  await expect(main).toContainText('要动哪些范围？')
  await expect(main).toContainText('→ TUI')
  // A header is a lookup key, not display copy.
  const text = await main.innerText()
  expect(text).not.toContain('交付方式')
  expect(text).not.toContain('修改范围')
  await shot(page, 'message-cards-ask-decided')
})

test('the composer’s view mode keeps the answers and hides the run’s cards, and survives a reload', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'message-cards-view')
  const now = Math.floor(Date.now() / 1000)
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-cards-view', '${session}', 'look at the tree', 'done', ${now - 60}, ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', 'run-e2e-cards-view', 'user', 'look at the tree', ${now - 60});
    ${assistantRow(session, 'run-e2e-cards-view', [
      { id: 'call-view', name: 'shell', args: { command: 'ls' } },
    ], now - 50)}
    ${toolRow(session, 'run-e2e-cards-view', 'call-view', 'shell', 'ran ls', 'README.md', now - 45)}
    ${saidRow(session, 'run-e2e-cards-view', 'the listing is empty', now - 40)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const chip = page.locator('[data-testid="view-mode-chip"]')
  const main = page.locator('.chat-shell main')
  await expect(chip).toContainText('默认')
  await expect(main).toContainText('ran ls')
  await expect(main).toContainText('the listing is empty')

  // Reading mode, not a delete: the run's cards step out, the answer stays.
  await chip.click()
  await page.locator('[data-testid="view-mode-option-concise"]').click()
  await expect(chip).toContainText('简洁')
  await expect(main).not.toContainText('ran ls')
  await expect(main).toContainText('the listing is empty')

  // The choice belongs to this conversation: a reload lands back in it.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="view-mode-chip"]')).toContainText('简洁')
  await expect(page.locator('.chat-shell main')).not.toContainText('ran ls')
  await expect(page.locator('.chat-shell main')).toContainText('the listing is empty')
  await shot(page, 'message-cards-view-concise')
})
