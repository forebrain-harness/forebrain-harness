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
 * A web_search gate is parked beside it in the same conversation: this plan
 * changed the fetch card alone, so the older card must still be the older card.
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

test('a fetch gate asks about the page, and a search gate keeps its older card', async ({ page }) => {
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
  // The question, then the page itself on its own line, unshortened.
  await expect(gate).toContainText('要我向公开网络取这个页面吗？')
  await expect(page.locator('[data-testid="web-fetch-page"]')).toHaveText(url)
  // The three answers, each saying what it does; the one that remembers names
  // the host it remembers, so the card never explains that a second time.
  await expect(gate.locator('[data-testid="pending-decision-accept"]')).toContainText('取这个页面')
  await expect(gate.locator('[data-testid="pending-decision-accept_and_remember"]')).toContainText('不再询问 example.com')
  await expect(gate.locator('[data-testid="pending-decision-cancel"]')).toContainText('不要取')
  await expect(gate).not.toContainText('权限记忆')

  const main = page.locator('.chat-shell main')
  const text = await main.innerText()
  expect(text).not.toContain(fetchId)
  expect(text).not.toContain('web_fetch')

  // The older cards are untouched by this change: a search gate is still its
  // tool's name and the runtime's own reason, and it still owns the
  // permission-memory box.
  const searchCard = page.locator(`[data-testid="pending-approval"][data-action-id="${searchId}"]`)
  await expect(searchCard).toBeVisible()
  await expect(searchCard).toContainText('web_search')
  await expect(searchCard).toContainText('权限记忆')

  await shot(page, 'web-fetch-approval-card')
})
