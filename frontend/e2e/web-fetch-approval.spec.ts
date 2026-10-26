import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

import { seedSessionInRoot, signIn, shot } from './support'

/**
 * The web's fetch gate, proven on the real page: a run parked on a web_fetch
 * approval is asked on the call it holds — the card's head says what page it
 * is fetching, the gate's footer asks the question the terminal's block asks,
 * and the three answers say what they do — never the tool's name or an action
 * id, and never a second box at the thread's end (plan 138).
 *
 * The gated call, its events and the parked action are seeded straight into
 * the gateway's state database, the same rows a live gated fetch leaves
 * behind; the permission facts (permission_input, prefix_rule_content,
 * available_decisions) are derived by the gateway from those rows, exactly as
 * they are for a live gate.
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

/** One pending tool approval, as the gate leaves it behind. */
function parkAction(session: string, kind: string, actionId: string, payload: unknown): string {
  const now = Math.floor(Date.now() / 1000)
  sqlite(`
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
      VALUES('${actionId}', '${session}', '${kind}', 'pending', '${sqlJson(payload)}', ${now - 60}, ${now - 60});
  `)
  return actionId
}

/** A waiting call and its gate: the assistant row that issued it, the started
 *  step (awaiting the reader), and the approval naming the call it holds. */
function parkGatedCall(
  session: string,
  runId: string,
  callId: string,
  toolName: string,
  _args: Record<string, unknown>,
  actionId: string,
  header: { action: string; target?: string },
  actionPayload: Record<string, unknown>,
): void {
  const now = Math.floor(Date.now() / 1000)
  const parts = [{
    type: 'tool_calls',
    tool_calls: [{
      id: callId,
      type: 'function',
      function: { name: toolName, arguments: JSON.stringify(_args) },
    }],
  }]
  // The engine's rows carry the call's input in its meta; the head reads the
  // object of its sentence (the page's URL) from there.
  const meta = { tool_name: toolName, status: 'awaiting approval', input: _args }
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('${runId}', '${session}', 'read the pricing page', 'waiting_action', ${now - 60}, ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', '${runId}', 'user', 'read the pricing page', ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, parts, created_at)
      VALUES('${session}', '${runId}', 'assistant', '', '${sqlJson(parts)}', ${now - 55});
    INSERT INTO fb_messages(session_id, run_id, role, content, parts, tool_step_id, tool_meta_json, created_at)
      VALUES('${session}', '${runId}', 'tool', '${sqlJson({})}', '${sqlJson([
        { tool_call_id: callId, type: 'tool_result_meta' },
        { type: 'tool_display', summary: 'fetching', body: '', tool_meta_json: JSON.stringify(meta) },
      ])}', '${callId}', '${sqlJson(meta)}', ${now - 50});
    INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
      VALUES('${session}', '${runId}', 'e2e-${callId}-start', 'tool_call_started', '${sqlJson({
        step_id: callId,
        tool_name: toolName,
        header_parts: header,
        tool_meta: { tool_name: toolName, status: 'awaiting approval' },
      })}', ${now - 50} * 1000);
    INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
      VALUES('${session}', '${runId}', 'e2e-${callId}-gate', 'approval_requested', '${sqlJson({
        action_id: actionId,
        action_kind: toolName,
        tool_step_id: callId,
        message: 'Awaiting approval or authorization to continue',
      })}', ${now - 49} * 1000);
  `)
  parkAction(session, toolName, actionId, actionPayload)
}

test('a fetch gate hangs on the fetching call and asks about its page', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-web-fetch')
  const url = 'https://example.com/pricing'
  parkGatedCall(session, 'run-e2e-webfetch', 'call-fetch', 'web_fetch', { url }, 'act-e2e-webfetch',
    { action: 'Fetching', target: url }, { url, rule_content: 'domain:example.com' })
  parkAction(session, 'web_search', 'act-e2e-websearch', { query: 'gateway session cookie' })

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })

  // The gate is the call's own card: its head says what page it is fetching,
  // the footer asks the question, and nothing waits at the thread's end.
  const gate = page.locator('.tool-card').filter({ has: page.locator('[data-testid="approval-gate-web_fetch"]') })
  await expect(gate).toBeVisible()
  await expect(gate.locator('.ttl')).toContainText('正在抓取')
  await expect(gate.locator('.ttl')).toContainText(url)
  await expect(gate).toContainText('要我去取这个页面吗？')
  await expect(page.locator('[data-testid="pending-approval"][data-action-id="act-e2e-webfetch"]')).toHaveCount(0)
  // The three answers, each saying what it does; the one that remembers names
  // the host it remembers, so the card never explains that a second time.
  await expect(gate.locator('[data-testid="pending-decision-accept"]')).toContainText('取这个页面')
  await expect(gate.locator('[data-testid="pending-decision-accept_and_remember"]')).toContainText('不再询问 example.com')
  await expect(gate.locator('[data-testid="pending-decision-cancel"]')).toContainText('不要取')
  await expect(gate).not.toContainText('权限记忆')
  // The node says the call waits on the reader — the clock.
  await expect(page.locator('.activity-step .tool-status--awaiting .lucide-clock')).toHaveCount(1)

  const main = page.locator('.chat-shell main')
  const text = await main.innerText()
  expect(text).not.toContain('act-e2e-webfetch')
  expect(text).not.toContain('web_fetch')

  // A search gate with no card of its own answers in the thread's tail frame
  // (prototype :1275, :3418): the semantic question, the fact line naming the
  // query and the cap, no permission-memory box, and never the tool's
  // internal name.
  const searchCard = page.locator(`[data-testid="pending-approval"][data-action-id="act-e2e-websearch"]`)
  await expect(searchCard).toBeVisible()
  await expect(searchCard).toContainText('要我搜一下公开网络吗？')
  await expect(searchCard).toContainText('“gateway session cookie” · 最多 5 条')
  await expect(searchCard).not.toContainText('权限记忆')
  await expect(searchCard).not.toContainText('web_search')

  await shot(page, 'web-fetch-approval-card')
})

/**
 * The routed fetch's two facts reach the screen. A run whose web_fetch was
 * routed at a provider leaves a payload naming the route and — when the
 * operator opened loopback space — the flag, which are the only two things the
 * card cannot learn from the URL. Seeded with no card of its own, so the
 * thread's tail frame carries it: the question as its head, the facts as its
 * panel (pkg/tool/web_tools.go writes routed_provider / allow_private_ip into
 * the gate payload).
 */
test('a routed fetch gate says who fetches it and that private space is open', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-web-fetch-routed')
  const url = 'https://example.com/pricing'
  parkAction(session, 'web_fetch', 'act-e2e-webfetch-routed', {
    url,
    rule_content: 'domain:example.com',
    routed_provider: 'deepseek',
    allow_private_ip: true,
  })

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })

  const gate = page.locator('[data-testid="pending-approval"][data-action-id="act-e2e-webfetch-routed"]')
  await expect(gate).toBeVisible()
  await expect(gate).toContainText('要我去取这个页面吗？')
  await expect(gate.locator('[data-testid="approval-gate-fact"]')).toHaveText([
    '由 deepseek 在服务端取回',
    '私网地址已放开',
  ])

  await shot(page, 'web-fetch-approval-routed')
})
