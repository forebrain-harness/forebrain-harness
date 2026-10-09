import { execFileSync } from 'node:child_process'
import { mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

import { signIn, shot } from './support'

/**
 * The web's exit-plan approval, proven on the real page: the card the
 * terminal's overlay has always prompted with — the plan itself, the reviews
 * already collected, the four choices — served from the same shared approval
 * gate, plus the review flow running live against the configured provider
 * (fake or real; the harness swaps the provider, not the flow).
 *
 * A finished review is handed to the planner on both surfaces: the approval
 * closes as the handoff, silently — the handoff is internal plumbing, so no
 * line lands on the timeline and the card is not left open for a second
 * decision.
 *
 * The parked approval and its events are seeded straight into the gateway's
 * state database — the same shape a real plan-mode turn parks — because the
 * words a model writes cannot be scripted, and the states that matter here
 * (a review in flight, a reviewer working) are events, not model output.
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

/** One session event, at a moment given in epoch ms. */
function event(session: string, runId: string, eventId: string, type: string, payload: unknown, atMs: number): string {
  return `INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
    VALUES('${session}', '${runId}', '${eventId}', '${type}', '${sqlJson(payload)}', ${atMs});`
}

/** The project key the gateway resolves its plan directory by: the launch
 * project root — symlinks evaluated, exactly as memory.ProjectRoot resolves
 * it — with its separators turned into dashes (memory.ProjectKey). */
function projectKey(): string {
  const root = process.env.E2E_PROJECT_PARENT
  if (!root) throw new Error('E2E_PROJECT_PARENT must point at the gateway launch project')
  return realpathSync(root).replace(/[/\\:]/g, '-')
}

/** Write the plan the parked approval is asking about, under the state root
 * and project key the gateway itself resolves. */
function seedPlan(markdown: string): void {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const path = `${home}/workspace/plans/${projectKey()}/plan.md`
  mkdirSync(path.slice(0, path.lastIndexOf('/')), { recursive: true })
  writeFileSync(path, markdown)
}

/** A session parked on a pending exit-plan approval, with the plan in place.
 * Returns the ids the page will read. */
function parkExitPlan(session: string, suffix: string): { runId: string; actionId: string } {
  const now = Math.floor(Date.now() / 1000)
  const runId = `run-e2e-exitplan-${suffix}`
  const actionId = `act-e2e-exitplan-${suffix}`
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('${runId}', '${session}', 'plan the change', 'waiting_action', ${now - 60}, ${now - 60});
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
      VALUES('${actionId}', '${session}', 'exit_plan_mode', 'pending', '${sqlJson({ session_id: session })}', ${now - 60}, ${now - 60});
    INSERT INTO fb_run_waits(run_id, action_id, tool_name, tool_input_json, created_at, updated_at)
      VALUES('${runId}', '${actionId}', 'exit_plan_mode', '{}', ${now - 60}, ${now - 60});
  `)
  seedPlan('# 计划：退出计划模式的网页审批\n\n1. 先读 README。\n2. 再对照计划逐条检查。\n')
  return { runId, actionId }
}

test('a parked exit-plan approval shows its plan and choices, not raw ids', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)
  parkExitPlan(session, 'idle')
  // The seeded plan must be the one the gateway itself resolves — the same
  // scope the approval-request read uses — or the card would have no plan to
  // show and a review nothing to review.
  const plan = await page.request.get(`/api/chat/sessions/${session}/plan-md`)
  expect(String((await plan.json())?.markdown ?? '')).toContain('再对照计划逐条检查')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const card = page.locator('[data-testid="exit-plan-approval"]')
  await expect(card).toBeVisible()

  const main = page.locator('.chat-shell main')
  // The card names itself and carries the plan itself — never the tool name,
  // an action id, or a path.
  await expect(card).toContainText('退出计划模式')
  await expect(card).toContainText('等待你的决定')
  await expect(page.locator('[data-testid="exit-plan-text"]')).toContainText('再对照计划逐条检查')
  const text = await main.innerText()
  expect(text).not.toContain('exit_plan_mode')
  expect(text).not.toContain('act-e2e-exitplan-idle')
  // The four choices, in the overlay's order, and the feedback row.
  await expect(page.locator('[data-testid="exit-plan-approve-clear"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-approve"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-keep-planning"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-ask-review"]')).toBeVisible()
  await expect(card.locator('input')).toHaveAttribute('placeholder', '继续规划时告诉规划模型要改什么（可选）')
  await shot(page, 'exit-plan-approval-card')
})

test('a review in flight is an event away: the card says so and the reviewer has its own view', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)
  const { runId, actionId } = parkExitPlan(session, 'running')
  const now = Math.floor(Date.now() / 1000)
  const reviewId = 'plan-review:e2e-running'
  sqlite(`
    ${event(session, runId, 'e2e-exitplan-started', 'plan_review_started', {
      action_id: actionId, review_id: reviewId,
      provider: 'deepseek', model: 'deepseek-chat',
    }, (now - 20) * 1000)}
    ${event(session, runId, 'e2e-exitplan-spawn', 'subagent_spawned', {
      agent_id: 'plan-review-e2e-1', agent_type: 'plan-reviewer', task_id: 'plan-review-e2e-1',
      title: 'Plan review', task: 'review the plan against the README', execution_id: 'exec-e2e-review',
      parent_run_id: runId, parent_tool_call_id: reviewId, task_index: 0,
    }, (now - 19) * 1000)}
    ${event(session, runId, 'e2e-exitplan-tool', 'tool_call_started', {
      step_id: 'call-e2e-review-read', tool_name: 'read', summary: 'reading the README',
      tool_meta: { tool_name: 'read', status: 'running', agent_id: 'plan-review-e2e-1', invocation: 'read README.md' },
    }, (now - 15) * 1000)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const card = page.locator('[data-testid="exit-plan-approval"]')
  await expect(card).toBeVisible()
  // The review running is a fact of the conversation's events; the card says
  // which model is at it and offers the stop the roster offers.
  await expect(card).toContainText('正在评审这份计划')
  await expect(page.locator('[data-testid="exit-plan-stop-review"]')).toBeVisible()
  await shot(page, 'exit-plan-approval-reviewing')

  // The timeline's plan-reviewer card is the way into the reviewer's own
  // view, where its tool cards live — nothing about the review leaks into
  // the conversation as text.
  const reviewCard = page.locator('[data-testid="subagent-call-card"]')
  await expect(reviewCard.first()).toContainText('1 个 plan-reviewer 任务')
  const rows = page.locator('[data-testid="subagent-task-row"]')
  await rows.nth(0).click()
  await expect(page.getByRole('button', { name: '返回' })).toBeVisible()
  await expect(page.locator('.chat-shell main')).toContainText('review the plan against the README')
  await expect(page.locator('.chat-shell main')).toContainText('read README.md')
  await shot(page, 'exit-plan-reviewer-view')
})

test('asking for a review hands it to the planner: the card closes silently', async ({ page }) => {
  test.info().setTimeout(240_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)
  parkExitPlan(session, 'live')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const card = page.locator('[data-testid="exit-plan-approval"]')
  await expect(card).toBeVisible()

  // Ask the configured model to review; the request itself must name the
  // pending approval it is about.
  await page.locator('[data-testid="exit-plan-ask-review"]').click()
  const model = page.locator('[data-testid="plan-review-model"]').first()
  await expect(model).toBeVisible()
  const request = page.waitForRequest((req) => req.url().includes('/plan-review') && req.method() === 'POST')
  await model.click()
  const reviewRequest = await request
  expect(reviewRequest.url()).toContain('act-e2e-exitplan-live')
  expect(await reviewRequest.postDataJSON()).toMatchObject({ model: expect.any(String) })

  // The review runs as its own subagent on the conversation's timeline…
  await expect(page.locator('[data-testid="subagent-call-card"]').first()).toBeVisible({ timeout: 120_000 })

  // …and a finished review is delivered, not shown for a second decision: the
  // handoff is internal plumbing, so the card the review was asked from
  // leaves with no line behind it. (The revised plan comes back as a new
  // approval of its own.)
  await expect(card).toHaveCount(0, { timeout: 180_000 })
  await shot(page, 'exit-plan-approval-delivered')
})

test('approving with the context cleared says so on the wire and closes the card', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)
  parkExitPlan(session, 'approve')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="exit-plan-approval"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-approve-clear"]')).toBeVisible()

  const approve = page.waitForRequest((req) => req.url().includes('/approve') && req.method() === 'POST')
  await page.locator('[data-testid="exit-plan-approve-clear"]').click()
  const approveBody = await (await approve).postDataJSON()
  expect(approveBody?.clear_context).toBe(true)
  await expect(page.locator('[data-testid="exit-plan-approval"]')).toHaveCount(0, { timeout: 30_000 })
  await shot(page, 'exit-plan-approval-approved')
})
