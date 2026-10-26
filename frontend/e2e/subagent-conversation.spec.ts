import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

import { seedSessionInRoot, signIn, shot } from './support'

/**
 * A subagent's own conversation, proven on the real page: it opens in the
 * conversation's right-hand drawer, the composer there talks to that subagent
 * (never to the conversation), its tab names the model and the context it runs
 * on, a message the user sends it is drawn as the user's own, a command that
 * belongs to the conversation answers with one sentence, Escape collapses the
 * drawer and an idle subagent takes no input at all — the same semantics the
 * terminal's subagent view has (plan 143).
 */

const realLLM = process.env.E2E_REAL_LLM === '1'

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
  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-subagent')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'delegate the probe [[e2e:subagent-net]]')
  await page.press('textarea', 'Enter')

  // The main agent dispatched the probe; its card's task row is the way into
  // that subagent's own conversation, which opens in the drawer beside the
  // conversation rather than in place of it.
  await page.waitForSelector('[data-testid="subagent-call-card"]', { timeout: 90_000 })
  await shot(page, 'subagent-conversation-card')
  await page.locator('[data-testid="subagent-task-row"]').first().click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()

  // The tab's composer bar says which context window this subagent runs on:
  // the percentage alone, in the model-and-context pill.
  const budget = drawer.locator('[data-testid="subagent-budget"]')
  await expect(budget).toBeVisible({ timeout: 30_000 })
  await expect(budget).toHaveText(/^\d+%$/)
  await shot(page, 'subagent-conversation-view')

  // A command hidden in a subagent's tab answers with one sentence, and no
  // request leaves the page. The slash menu owns Enter while it is open (it
  // picks a command), so a slash line is submitted with the submit button.
  // /compact is one of them now: a subagent compacts nothing, and its own
  // compaction card is never drawn (plan 142 deleted the endpoint, plan 143
  // the page's call to it).
  const tabInput = drawer.locator('textarea')
  await tabInput.fill('/compact')
  await drawer.locator('button[aria-label="Send"]').click()
  await expect(drawer).toContainText('Run this from the main view')
  await expect(drawer.locator('.compaction-card')).toHaveCount(0)
  await shot(page, 'subagent-conversation-hidden-command')

  await tabInput.fill('/new')
  await drawer.locator('button[aria-label="Send"]').click()
  await expect(drawer).toContainText('Run this from the main view')

  // A message sent from this tab reaches the subagent, and only the subagent.
  const reply = process.env.E2E_REPLY_OK ?? 'E2E_REPLY_OK'
  await tabInput.fill('continue')
  await tabInput.press('Enter')
  // The probe's subagent is still resending its dropped first request (the
  // run's own network retry: ten attempts, backoff up to 32s), and a queued
  // steer only rides the NEXT model call — waiting the retry out takes
  // minutes. The queue's printed way out is the interrupt: it abandons the
  // in-flight resend and delivers the queue at once.
  await drawer.locator('[data-testid="queue-interrupt"]').click()
  await expect(drawer).toContainText(reply, { timeout: 90_000 })
  await expect(drawer.locator('[data-testid="subagent-user-message"]').last()).toContainText('continue')
  // Nothing that happened in the drawer leaked into the conversation.
  await expect(page.locator('.chat-shell main')).not.toContainText(reply)
  await shot(page, 'subagent-conversation-continued')

  // Its execution ended: an idle subagent takes no messages — the agent that
  // dispatched it decides what it does next — so the tab has no input box at
  // all, and what is left where the composer's bar was reads the run it
  // finished (the reading mode and the pill).
  await expect(drawer.locator('[data-testid="subagent-idle-bar"]')).toBeVisible({ timeout: 30_000 })
  await expect(drawer.locator('textarea')).toHaveCount(0)
  await expect(drawer.locator('[data-testid="subagent-idle-bar"] [data-testid="view-mode-chip"]')).toBeVisible()
  await shot(page, 'subagent-conversation-idle')

  // Escape in the drawer collapses it, and the subagent's answer is not in the
  // conversation it shares the page with.
  await drawer.locator('[data-testid="subagent-idle-bar"] [data-testid="view-mode-chip"]').focus()
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-testid="subagent-drawer"]')).toHaveCount(0)
  await expect(page.locator('.chat-shell main')).not.toContainText(reply)
  await shot(page, 'subagent-conversation-back')
})

/**
 * A steer queued in a subagent's tab waits for its next tool boundary; once the
 * subagent takes it there, the tab's queue lets it go at once and it is drawn as
 * the user's own message — while the subagent is still running, so no
 * execution-end refresh can be what clears it.
 */
test('a steer the subagent takes at its tool boundary leaves its queue at once', async ({ page }) => {
  test.skip(realLLM, 'the subagent-steer probe drives the fake provider')
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-subagent')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'delegate the slow probe [[e2e:subagent-steer]]')
  await page.press('textarea', 'Enter')

  await page.waitForSelector('[data-testid="subagent-call-card"]', { timeout: 90_000 })
  await page.locator('[data-testid="subagent-task-row"]').first().click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()

  // The subagent is inside its 8-second shell call: the message waits in the
  // tab's queue for the call to return.
  const steer = 'retries must stay invisible to the user'
  const tabInput = drawer.locator('textarea')
  await tabInput.fill(steer)
  await tabInput.press('Enter')
  const queue = drawer.locator('[data-testid="pending-queue"]')
  await expect(queue).toContainText(steer)
  // The queue fades in; shoot it once the fade is over.
  await expect(queue.locator('..')).toHaveCSS('opacity', '1')
  await shot(page, 'subagent-steer-queued')

  // The call returns and the subagent takes the steer, then holds its answer
  // for 30 seconds: the queue must be gone well inside that window.
  await expect(drawer.locator('[data-testid="subagent-user-message"]').last()).toContainText(steer, { timeout: 30_000 })
  await expect(queue).toHaveCount(0)
  await expect(drawer).toContainText('steer received')
  await shot(page, 'subagent-steer-delivered')
})

test('a plan-reviewer tab opens with its own review model and budget', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-subagent')
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
  // The conversation has no call card for a review it did not draw: the top
  // bar's subagent control is the way in.
  const toggle = page.locator('[data-testid="subagent-drawer-toggle"]')
  await expect(toggle).toBeVisible({ timeout: 30_000 })
  await toggle.click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer.locator('[data-testid="subagent-tab-plan-reviewer-e2e"]')).toBeVisible()

  // The percentage is the pill's own; the window and the model are in the card
  // it opens, read from the spawn — not the conversation's.
  const budget = page.locator('[data-testid="subagent-budget"]')
  await expect(budget).toBeVisible({ timeout: 30_000 })
  await expect(budget).toHaveText('65%')
  await page.locator('[data-testid="subagent-run-pill"]').click()
  const card = page.locator('[data-testid="subagent-run-card"]')
  await expect(card).toContainText('zhipuai/glm-5.3-flash')
  await expect(card.locator('[data-testid="subagent-context-window"]')).toHaveText('1M')
  await shot(page, 'subagent-conversation-plan-reviewer')
})

test('a subagent run hangs on the activity line, and its cut head opens in the drawer', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-subagent')
  const now = Math.floor(Date.now() / 1000)
  const command = 'go vet -tags fts5 $(go list ./pkg/... | grep -v third_party) && go test -tags fts5 -count=1 -race ./pkg/tool ./pkg/tui ./pkg/safety ./pkg/gateway'
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-sub-line', '${session}', 'check the tree', 'done', ${now - 60}, ${now - 60});
    ${event(session, 'run-e2e-sub-line', 'e2e-sub-line-spawn', 'subagent_spawned', {
      agent_id: 'subagent-e2e-line', agent_type: 'explore', task_id: 'subagent-e2e-line',
      title: 'check the tree', task: 'run the checks', execution_id: 'exec-sub-line',
    }, (now - 40) * 1000)}
    ${event(session, 'run-e2e-sub-line', 'e2e-sub-line-tool-start', 'tool_call_started', {
      step_id: 'call-sub-line', tool_name: 'shell', summary: `ran ${command}`,
      input: { command },
      tool_meta: { tool_name: 'shell', status: 'running', agent_id: 'subagent-e2e-line' },
    }, (now - 25) * 1000)}
    ${event(session, 'run-e2e-sub-line', 'e2e-sub-line-tool-done', 'tool_call_completed', {
      step_id: 'call-sub-line', tool_name: 'shell', summary: `ran ${command}`, display_body: 'ok',
      duration_seconds: 4.2,
      tool_meta: { tool_name: 'shell', status: 'completed', agent_id: 'subagent-e2e-line' },
    }, (now - 20) * 1000)}
    ${event(session, 'run-e2e-sub-line', 'e2e-sub-line-end', 'subagent_ended', {
      agent_id: 'subagent-e2e-line', agent_type: 'explore', task_id: 'subagent-e2e-line',
      status: 'ok', execution_id: 'exec-sub-line', finished_at_ms: (now - 15) * 1000,
    }, (now - 15) * 1000)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const toggle = page.locator('[data-testid="subagent-drawer-toggle"]')
  await expect(toggle).toBeVisible({ timeout: 30_000 })
  await toggle.click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()
  await expect(drawer.locator('[data-testid="subagent-tab-subagent-e2e-line"]')).toBeVisible()

  // The subagent's own run hangs on the activity line; its call's state is
  // the call's node.
  const view = drawer.locator('[role="log"]')
  await expect(view.locator('.activity-line')).toHaveCount(1)
  await expect(view.locator('.tool-status--node.tool-status--done')).toHaveCount(1)

  // The one-line head is cut in the drawer's narrow column: the eye opens the
  // whole command as a document tab of this same drawer.
  const eye = view.locator('[data-testid="tool-show-all"]').first()
  await expect(eye).toBeVisible()
  await eye.click()
  // (The tab element alone: its close button's testid shares the prefix.)
  const docTab = drawer.locator('.subagent-drawer-tab[data-testid^="doc-tab-"]')
  await expect(docTab).toHaveCount(1)
  await expect(docTab).toContainText(`命令 · ${command.slice(0, 20)}`)
  await expect(drawer.locator('[data-testid="tool-doc-command"]')).toBeVisible()
  await shot(page, 'subagent-conversation-doc-tab')
})
