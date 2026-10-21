import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

import { seedSessionInRoot, signIn, shot } from './support'

/**
 * The web's fetch gate, proven on the real page: a run parked on a web_fetch
 * approval is asked the question the terminal's block asks - which page, and
 * what to do about it - so the card carries the question, the page itself and
 * the three answers, and never the tool's name or an action id.
 *
 * The parked actions are seeded straight into the gateway's state database, the
 * same rows a real gated tool call leaves behind; the permission facts
 * (permission_input, prefix_rule_content, available_decisions) are derived by
 * the gateway from those rows, exactly as they are for a live gate.
 *
 * A web_search gate is parked beside it in the same conversation: search rides
 * the shared approval card now, so it is asserted as that card draws it — the
 * semantic question, the fact line, and never the tool's internal name.
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

test('a fetch gate asks about the page, and a search gate asks its own question', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-web-fetch')
  const url = 'https://example.com/pricing'
  const fetchId = parkAction(session, 'web_fetch', 'act-e2e-webfetch', { url, rule_content: 'domain:example.com' })
  const searchId = parkAction(session, 'web_search', 'act-e2e-websearch', { query: 'gateway session cookie' })

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })

  const gate = page.locator('[data-testid="web-fetch-approval"]')
  await expect(gate).toBeVisible()
  // The question, then the page itself on its own line, unshortened. The
  // question names no access route: a fetch may be served by a routed provider,
  // so promising "the public web" here would be a claim the card cannot keep.
  await expect(gate).toContainText('要我去取这个页面吗？')
  await expect(page.locator('[data-testid="web-fetch-page"]')).toHaveText(url)
  // The three answers, each saying what it does; the one that remembers names
  // the host it remembers, so the card never explains that a second time.
  await expect(gate.locator('[data-testid="pending-decision-accept"]')).toContainText('取这个页面')
  await expect(gate.locator('[data-testid="pending-decision-accept_and_remember"]')).toContainText('不再询问 example.com')
  await expect(gate.locator('[data-testid="pending-decision-cancel"]')).toContainText('不要取')
  await expect(gate).not.toContainText('权限记忆')
  // A plain fetch carries neither routing fact in its payload, so the card
  // draws no fact row at all: it is the card it always was.
  await expect(gate.locator('[data-testid="web-fetch-fact"]')).toHaveCount(0)

  const main = page.locator('.chat-shell main')
  const text = await main.innerText()
  expect(text).not.toContain(fetchId)
  expect(text).not.toContain('web_fetch')

  // A search gate is the shared approval card now (prototype :1275, :3418): the
  // semantic question, the fact line naming the query and the cap, no
  // permission-memory box, and never the tool's internal name.
  const searchCard = page.locator(`[data-testid="pending-approval"][data-action-id="${searchId}"]`)
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
 * card cannot learn from the URL. Seeded the same way as the gate above: the
 * payload is the row a live routed gate writes (pkg/tool/web_tools.go
 * writes routed_provider / allow_private_ip into the gate payload).
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

  const gate = page.locator('[data-testid="web-fetch-approval"]')
  await expect(gate).toBeVisible()
  await expect(gate).toContainText('要我去取这个页面吗？')
  await expect(gate.locator('[data-testid="web-fetch-fact"]')).toHaveText([
    '由 deepseek 在服务端取回',
    '私网地址已放开',
  ])

  await shot(page, 'web-fetch-approval-routed')
})
