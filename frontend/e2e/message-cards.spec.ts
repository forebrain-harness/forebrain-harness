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
  input?: Record<string, unknown>,
): string {
  const meta = { tool_name: toolName, status: 'completed', ...(input ? { input } : {}) }
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

test('a long call keeps a one-line header; the whole command opens from the eye', async ({ page }) => {
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
    ${toolRow(session, 'run-e2e-cards-head', 'call-long', 'shell', longSummary, 'ok', now - 40, { command })}
    ${toolRow(session, 'run-e2e-cards-head', 'call-short', 'shell', shortSummary, 'README.md', now - 39, { command: 'ls' })}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(headers(page)).toHaveCount(2)

  // The head stays one line however long the command is: the title area of
  // the long call is exactly as tall as the short one's — one line, cut with
  // an ellipsis, never wrapped into a paragraph. (The head row itself may be
  // taller on the long card: the eye that opens the whole command is a 24px
  // square beside the title, as in the prototype's .tt-all.)
  const longTitle = headers(page).nth(0).locator('.ttl')
  const shortTitle = headers(page).nth(1).locator('.ttl')
  await expect(longTitle).toHaveCSS('white-space', 'nowrap')
  expect((await longTitle.boundingBox())?.height).toBeCloseTo((await shortTitle.boundingBox())?.height ?? 0, 1)

  // A Chinese interface words the head in Chinese: the action word is 已运行,
  // the command itself stays as it ran.
  await expect(headers(page).nth(0).locator('.verb')).toHaveText('已运行')
  await expect(headers(page).nth(0).locator('.tgt')).toContainText('go vet')

  // The body is the result only: no command block under any head, and no
  // native tooltip on the cut line — the eye is the way to see the whole.
  await headers(page).nth(0).click()
  await expect(page.locator('.cmd-full')).toHaveCount(0)
  await expect(headers(page).nth(0).locator('span[title]')).toHaveCount(0)
  await expect(page.locator('.tool-card-body').first()).not.toContainText(command)

  // The truncated head carries the eye; it opens the command as a document
  // tab of the drawer, labeled 命令 · <first line>, and the tab's page holds
  // the steps of the chain.
  const eye = headers(page).nth(0).locator('[data-testid="tool-show-all"]')
  await expect(eye).toBeVisible()
  await eye.click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()
  // (The tab element alone: its close button's testid shares the prefix.)
  const docTab = drawer.locator('.subagent-drawer-tab[data-testid^="doc-tab-"]')
  await expect(docTab).toContainText(`命令 · ${command.slice(0, 20)}`)
  await expect(drawer.locator('[data-testid="tool-doc-command"]')).toBeVisible()
  // The chain reads one step per row: the pipe inside $( ), then the &&.
  await expect(drawer.locator('[data-testid="tool-doc-command"] .step')).toHaveCount(3)
  await shot(page, 'message-cards-tool-head')

  // In English the head says what the engine said: Ran, not a translation.
  // (A fresh page rather than a reload: this page's init script puts the
  // interface back into Chinese on every navigation, and the new page — same
  // context, so the same sign-in — reads the locale the storage now holds.)
  await page.evaluate(() => localStorage.setItem('forebrain-locale', 'en'))
  const enPage = await page.context().newPage()
  await enPage.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(enPage.locator('.chat-shell main [data-slot="collapsible-trigger"]').first().locator('.verb')).toHaveText('Ran')
  await enPage.close()
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

// The card's footer prints Esc; this is Esc, on the real page, through the real
// gateway: the key sends the engine the `cancel` decision the terminal's overlay
// returns (POST /api/actions/:id/approve with decision cancel), so a question
// nobody will answer stops parking the run. The footer also prints ←/→ — those
// are the same session's card, so the arrows are proven here too.
test('Esc on a question card cancels the question the way the footer says it does', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'message-cards-ask-esc')
  const now = Math.floor(Date.now() / 1000)
  const actionId = 'act-e2e-cards-ask-esc'
  // Two questions, so the arrows have somewhere to go before the Esc.
  sqlite(`
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
      VALUES('${actionId}', '${session}', 'user_interaction', 'pending', '${sqlJson({
        questions: [
          {
            id: '交付方式',
            header: '交付方式',
            prompt: '这次改动怎么交付？',
            options: [{ id: 'design', label: '设计方案并开发实施' }, { id: 'direct', label: '直接改' }],
            allow_multiple: false,
            allow_other: true,
          },
          {
            id: '修改范围',
            header: '修改范围',
            prompt: '要动哪些范围？',
            options: [{ id: 'tui', label: 'TUI' }, { id: 'web', label: 'Web' }],
            allow_multiple: false,
            allow_other: true,
          },
        ],
      })}', ${now - 60}, ${now - 60});
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const card = page.locator('[data-testid="ask-card"]')
  await expect(card).toBeVisible()
  await expect(card).toContainText('这次改动怎么交付？')

  // ←/→ move a question, as the footer's "点标签或 ←/→ 切题" promises.
  await card.press('ArrowRight')
  await expect(card).toContainText('要动哪些范围？')
  await card.press('ArrowLeft')
  await expect(card).toContainText('这次改动怎么交付？')

  // Esc cancels the question: the decision the engine understands, not a
  // dismissed card the run would keep waiting on.
  const cancelled = page.waitForRequest((req) =>
    req.url().includes(`/api/actions/${actionId}/approve`) && req.method() === 'POST')
  await card.press('Escape')
  expect((await (await cancelled).postDataJSON())?.decision).toBe('cancel')
  await expect(page.locator('[data-testid="pending-question"]')).toHaveCount(0, { timeout: 30_000 })
  await shot(page, 'message-cards-ask-escaped')
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
  // The head carries the verb alone, in the interface's own language — the
  // questions are the body's business.
  await expect(headers(page).first()).toContainText(/询问用户/)
  expect(await headers(page).first().innerText()).not.toContain('交付方式')

  // A settled question card is drawn open: the answer is the point of the
  // card, so it is not hidden behind a click (prototype :3113).
  const main = page.locator('.chat-shell main')
  await expect(page.locator('[data-testid="ask-done"]')).toBeVisible()
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
  // The head speaks the interface's language (owner 2026-10-25): the engine's
  // action word reads 已运行 here, and the command itself stays as it ran. The
  // words are the head's own — the node beside it names the state, never the
  // sentence the head already says.
  await expect(main).toContainText('已运行')
  await expect(page.locator('.tool-head .tgt')).toContainText('ls')
  await expect(main).toContainText('the listing is empty')

  // Reading mode, not a delete: the run's cards step out, the answer stays.
  await chip.click()
  await page.locator('[data-testid="view-mode-option-concise"]').click()
  await expect(chip).toContainText('简洁')
  await expect(main).not.toContainText('Ran ls')
  await expect(main).toContainText('the listing is empty')

  // The choice belongs to this conversation: a reload lands back in it.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="view-mode-chip"]')).toContainText('简洁')
  await expect(page.locator('.chat-shell main')).not.toContainText('Ran ls')
  await expect(page.locator('.chat-shell main')).toContainText('the listing is empty')
  await shot(page, 'message-cards-view-concise')
})

// A shell card's body is the result only (plan 137): the streams as rows,
// stderr red, the exit on the head, the truncation footer with the eye that
// opens the whole output in the drawer — and an edit's card opens with the
// unified diff rows. The rows are seeded in the engine's own card format
// (formatShellStep / turnDiffSection), so the gateway reads the same facts
// back a live card would carry.
test('a shell card shows only its output, and a truncated one opens the whole of it', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'message-cards-shell')
  const now = Math.floor(Date.now() / 1000)
  const command = 'go vet -tags fts5 ./pkg/gateway/... && go test -tags fts5 ./pkg/gateway/... -run Session'
  // What formatShellStep writes for a failed, truncated run: the two streams
  // in fences, the truncation note, then the output JSON carrying the exit
  // and the retrieval id.
  const shellBody = [
    '**shell**',
    '',
    'input:',
    '',
    '```json',
    `{"command": ${JSON.stringify(command)}}`,
    '```',
    '',
    'stdout:',
    '',
    '```text',
    'ok  github.com/forebrain-harness/forebrain-harness/pkg/gateway	1.204s',
    '```',
    '',
    'stderr:',
    '',
    '```text',
    'pkg/gateway/ws.go:81:15: too many arguments in call to ControlPlaneTokenFromRequest',
    '```',
    '',
    'preview truncated: stdout omitted 421888 bytes',
    '',
    'output:',
    '',
    '```json',
    '{',
    '  "exit_code": 1,',
    '  "stdout_omitted_bytes": 421888,',
    '  "retrieve_id": 31',
    '}',
    '```',
  ].join('\n')
  const editBody = [
    'turn diff: `frontend/src/lib/api.ts` (+1/-1)',
    '',
    '```diff',
    '--- frontend/src/lib/api.ts:before',
    '+++ frontend/src/lib/api.ts:after',
    '@@ -39,2 +39,2 @@',
    ' export async function request(path: string, init: RequestInit = {}) {',
    '-  const headers: Record<string, string> = {}',
    '+  const headers: Record<string, string> = {}',
    '```',
  ].join('\n')
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-cards-shell', '${session}', 'check the gateway', 'done', ${now - 60}, ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', 'run-e2e-cards-shell', 'user', 'check the gateway', ${now - 60});
    ${assistantRow(session, 'run-e2e-cards-shell', [
      { id: 'call-shell', name: 'shell', args: { command } },
      { id: 'call-edit', name: 'edit_file', args: { path: 'frontend/src/lib/api.ts' } },
    ], now - 50)}
    ${toolRow(session, 'run-e2e-cards-shell', 'call-shell', 'shell', `ran ${command}`, shellBody, now - 45, { command })}
    ${toolRow(session, 'run-e2e-cards-shell', 'call-edit', 'edit_file', 'edited frontend/src/lib/api.ts', editBody, now - 44)}
  `)

  // The whole output lives behind the spool: the drawer's page reads it from
  // the tool-output route, answered here with a fixed text.
  const fullText = ['ok  pkg/gateway	0.012s', '--- FAIL: TestSession', Array.from({ length: 40 }, (_, i) => `log line ${i + 1}`).join('\n')].join('\n')
  await page.route(/\/api\/chat\/sessions\/.+\/tool-output\/31$/, async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ command, text: fullText, spool_omitted_bytes: 0 }),
    })
  })

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(headers(page)).toHaveCount(2)
  const shellCard = page.locator('.tool-card').nth(0)
  await headers(page).nth(0).click()
  const body = shellCard.locator('.tool-card-body')
  await expect(body).toBeVisible()

  // The body is the result only: none of the Markdown the card once rendered.
  const bodyText = await body.innerText()
  expect(bodyText).not.toContain('**shell**')
  expect(bodyText).not.toContain('input:')
  expect(bodyText).not.toContain('output:')
  expect(bodyText).not.toContain('stdout:')
  expect(bodyText).not.toContain('stderr:')
  expect(bodyText).not.toContain('sandbox_backend')
  expect(bodyText).not.toContain('"command"')

  // stdout is grey mono, the stderr row red (prototype .ol.err).
  await expect(body.locator('.ol').filter({ hasText: 'too many arguments' })).toHaveCSS('color', 'rgb(180, 35, 24)')

  // The output panel scrolls inside itself at the prototype's 360px bound.
  await expect(body.locator('.out-scroll')).toHaveCSS('max-height', '360px')

  // The non-zero exit rides the head, and the footer names what the middle
  // omitted with the eye beside it.
  await expect(headers(page).nth(0)).toContainText('退出码 1')
  await expect(body.locator('[data-testid="shell-foot"]')).toContainText('中间 412 KB 已省略')
  const eye = body.locator('[data-testid="shell-open-output"]')
  await expect(eye).toBeVisible()
  await eye.click()

  // The whole output opens as a document tab of the drawer.
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()
  await expect(drawer.locator('.subagent-drawer-tab[data-testid^="doc-tab-"]')).toContainText('输出 · go vet')
  const doc = drawer.locator('[data-testid="tool-doc-output"]')
  await expect(doc).toBeVisible()
  await expect(doc.locator('pre')).toContainText('log line 40')
  await shot(page, 'message-cards-shell-body')

  // The edit's card opens with the unified diff rows, the added one
  // announced for screen readers.
  await headers(page).nth(1).click()
  const editBody2 = page.locator('.tool-card').nth(1).locator('.tool-card-body')
  await expect(editBody2.locator('[data-testid="diff-block"]')).toBeVisible()
  await expect(editBody2.locator('.dr.add')).toHaveCount(1)
  await expect(editBody2.locator('.dr.del')).toHaveCount(1)
  // The rows announce themselves for screen readers: 新增 / 删除.
  await expect(editBody2.locator('.sr-only').filter({ hasText: '新增' })).toHaveCount(1)
  await expect(editBody2.locator('.sr-only').filter({ hasText: '删除' })).toHaveCount(1)
  await shot(page, 'message-cards-edit-diff')
})
