import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

import { signIn, shot } from './support'

/**
 * A subagent's own view, proven on the real page: the composer there talks to
 * that subagent (never to the conversation), the view opens with its own
 * context window, a message the user sends it is drawn as the user's own, a
 * command that belongs to the conversation answers with one sentence, and
 * Escape returns to the conversation — the same semantics the terminal's
 * subagent view has.
 */

const realLLM = process.env.E2E_REAL_LLM === '1'

/**
 * The flaky proxy's control file. web_e2e.sh sets it only when a real run
 * routes the provider through the proxy (FOREBRAIN_E2E_REAL_LLM=1); it is
 * absent in fake mode, where the disconnect case has nothing to talk to.
 */
const flakyControl = process.env.E2E_FLAKY_CONTROL

/** Flip the flaky proxy's policy; the proxy re-reads this file per request. */
function setProxyPolicy(policy: 'pass' | 'drop-subagent' | 'drop-all'): void {
  if (!flakyControl) throw new Error('E2E_FLAKY_CONTROL must point at the flaky proxy control file')
  writeFileSync(flakyControl, `${policy}\n`)
}

/** The gateway's state database, by name under E2E_HOME. */
function stateDb(): string {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const found = execFileSync('find', [home, '-name', 'forebrain.state.sqlite'], { encoding: 'utf8' })
    .split('\n').map((line) => line.trim()).filter(Boolean)
  if (!found[0]) throw new Error('no state database found under E2E_HOME')
  return found[0]
}

function sqlite(sql: string): void {
  execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(), sql], { stdio: ['ignore', 'pipe', 'pipe'] })
}

function sqlJson(value: unknown): string {
  return JSON.stringify(value).replace(/'/g, "''")
}

function event(session: string, runId: string, eventId: string, type: string, payload: unknown, atMs: number): string {
  return `INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
    VALUES('${session}', '${runId}', '${eventId}', '${type}', '${sqlJson(payload)}', ${atMs});`
}

test('a subagent view talks to its own subagent', async ({ page }) => {
  test.skip(realLLM, 'the subagent-net probe drives the fake provider')
  test.info().setTimeout(240_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'delegate the probe [[e2e:subagent-net]]')
  await page.press('textarea', 'Enter')

  // The main agent dispatched the probe; its card is the way into its view.
  await page.waitForSelector('[data-testid="subagent-call-card"]', { timeout: 90_000 })
  await shot(page, 'subagent-conversation-card')

  // Open the subagent's own view from the tab strip it appears in.
  const subagentTab = page.locator('.forebrain-agent-tab').nth(1)
  await subagentTab.click()
  // The view opens with this subagent's own context window: N%/window.
  const budget = page.locator('[data-testid="subagent-budget"]')
  await expect(budget).toBeVisible({ timeout: 30_000 })
  await expect(budget).toHaveText(/^\d+%\/\S+$/)
  await shot(page, 'subagent-conversation-view')

  // A message sent from this view reaches the subagent, and only the subagent.
  const reply = process.env.E2E_REPLY_OK ?? 'E2E_REPLY_OK'
  await page.fill('textarea', 'continue')
  await page.press('textarea', 'Enter')
  await expect(page.locator('.chat-shell main')).toContainText(reply, { timeout: 90_000 })
  await expect(page.locator('[data-testid="subagent-user-message"]').last()).toContainText('continue')
  await shot(page, 'subagent-conversation-continued')

  // A command hidden in a subagent's view answers with one sentence, and no
  // request leaves the page. The slash menu owns Enter while it is open (it
  // picks a command), so a slash line is submitted with the submit button.
  await page.fill('textarea', '/new')
  await page.locator('button[aria-label="Submit"]').click()
  await expect(page.locator('.chat-shell main')).toContainText('Run this from the main view')
  await shot(page, 'subagent-conversation-hidden-command')

  // /compact acts on the subagent, and its own card is drawn in its view.
  await page.fill('textarea', '/compact')
  await page.locator('button[aria-label="Submit"]').click()
  await expect(page.locator('.compaction-card').first()).toBeVisible({ timeout: 90_000 })
  await shot(page, 'subagent-conversation-compacted')

  // Escape returns to the conversation, and the subagent's answer is not there.
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-testid="subagent-budget"]')).toHaveCount(0)
  await expect(page.locator('.chat-shell main')).not.toContainText(reply)
  await shot(page, 'subagent-conversation-back')
})

/**
 * The real-model counterpart of the view test above: the provider is reached
 * through the flaky proxy, so the test can cut the subagent's own request
 * mid-stream (a live provider will not do that on request) and then continue
 * it from its own view. It needs a real provider behind the proxy, so it skips
 * in fake mode — the CI gate, where no proxy is running.
 */
test('a subagent that lost the network is continued from its own view', async ({ page }) => {
  test.skip(!realLLM || !flakyControl, 'needs a real provider cut mid-stream through the flaky proxy')
  test.info().setTimeout(300_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  setProxyPolicy('pass')
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')

  // The main agent dispatches a general-purpose subagent and waits for it; the
  // dispatch's card is the way into its view.
  await page.fill(
    'textarea',
    'Dispatch one general-purpose subagent to read README.md and summarize it in three sentences, then wait for its result.',
  )
  await page.press('textarea', 'Enter')
  await page.waitForSelector('[data-testid="subagent-call-card"]', { timeout: 180_000 })
  await shot(page, 'subagent-conversation-net-card')

  // The network dies on the subagent's own request: its card must fail.
  setProxyPolicy('drop-subagent')
  const failedCard = page.locator('[data-testid="subagent-call-card"]').filter({ hasText: /failed|Failed/i }).first()
  await expect(failedCard).toBeVisible({ timeout: 180_000 })
  await shot(page, 'subagent-conversation-net-failed')

  // The network is back; the subagent's own view is where it is continued.
  setProxyPolicy('pass')
  await page.locator('.forebrain-agent-tab').nth(1).click()
  await expect(page.locator('[data-testid="subagent-budget"]')).toBeVisible({ timeout: 30_000 })

  const answers = page.locator('.chat-shell main .is-assistant')
  const before = await answers.allInnerTexts()
  await page.fill('textarea', 'continue')
  await page.press('textarea', 'Enter')

  // A real model's words are unknown, so the answer's text is not asserted —
  // but where it lands is. Wait for a fresh non-empty reply in THIS view,
  // remember it, then prove the same text never reaches the conversation.
  await expect
    .poll(async () => (await answers.allInnerTexts()).slice(before.length).map((text) => text.trim()).filter(Boolean).join('\n'), {
      timeout: 180_000,
    })
    .not.toBe('')
  await expect(page.locator('[data-testid="subagent-user-message"]').last()).toContainText('continue')
  const answer = (await answers.allInnerTexts()).slice(before.length).map((text) => text.trim()).filter(Boolean).join('\n')
  expect(answer.length).toBeGreaterThan(0)
  await shot(page, 'subagent-conversation-net-continued')

  // Escape returns to the conversation, and the subagent's answer is not there.
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-testid="subagent-budget"]')).toHaveCount(0)
  await expect(page.locator('.chat-shell main')).not.toContainText(answer)
  await shot(page, 'subagent-conversation-net-back')
})

test('a plan-reviewer view opens with its own review model and budget', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)
  const now = Math.floor(Date.now() / 1000)
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-review', '${session}', 'review the plan', 'done', ${now - 60}, ${now - 60});
    ${event(session, 'run-e2e-review', 'e2e-review-spawn', 'subagent_spawned', {
      agent_id: 'plan-reviewer-e2e', agent_type: 'plan-reviewer', task_id: 'plan-reviewer-e2e',
      title: 'Plan review', task: 'review the plan', execution_id: 'exec-review',
      model_provider: 'zhipuai', model: 'glm-5.3-flash',
    }, (now - 30) * 1000)}
    ${event(session, 'run-e2e-review', 'e2e-review-budget', 'token_budget_updated', {
      agent_id: 'plan-reviewer-e2e', percent_left: 35, context_window: 1_000_000, token_usage: 650_000,
    }, (now - 29) * 1000)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('.forebrain-agent-tab')
  await page.locator('.forebrain-agent-tab').nth(1).click()
  const budget = page.locator('[data-testid="subagent-budget"]')
  await expect(budget).toBeVisible({ timeout: 30_000 })
  await expect(budget).toHaveText('65%/1M')
  // The header names the review model, read from the spawn — not the
  // conversation's.
  await expect(page.locator('.chat-shell main')).toContainText('zhipuai/glm-5.3-flash')
  await shot(page, 'subagent-conversation-plan-reviewer')
})
