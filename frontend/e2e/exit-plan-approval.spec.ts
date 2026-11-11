import { execFileSync } from 'node:child_process'
import { mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

import { seedSessionInRoot, signIn, shot } from './support'

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

/** Write the plan the parked approval is asking about, under the state root,
 * the project key and the session directory the gateway itself resolves —
 * plans live per conversation (plans/<projectKey>/<session>/), so the seed
 * must land in the parked session's own directory. */
function seedPlan(session: string, markdown: string): void {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const path = `${home}/workspace/plans/${projectKey()}/${session}/plan.md`
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
  seedPlan(session, '# 计划：退出计划模式的网页审批\n\n1. 先读 README。\n2. 再对照计划逐条检查。\n')
  return { runId, actionId }
}

test('a parked exit-plan approval takes the input\'s place, with no card in the thread', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-exit-plan')
  parkExitPlan(session, 'idle')
  // The seeded plan must be the one the gateway itself resolves — the same
  // scope the approval-request read uses — or the card would have no plan to
  // show and a review nothing to review.
  const plan = await page.request.get(`/api/chat/sessions/${session}/plan-md`)
  expect(String((await plan.json())?.markdown ?? '')).toContain('再对照计划逐条检查')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const dock = page.locator('[data-testid="exit-plan-approval"]')
  await expect(dock).toBeVisible()

  const main = page.locator('.chat-shell main')
  // The plan approval is where the input was: the composer — its words, its
  // attachments — is gone from sight, the dock standing in its place.
  await expect(page.locator('.chat-composer textarea')).toBeHidden()
  await expect(dock).toContainText('计划')
  // The plan itself is the dock's body, never the tool name, an action id or
  // a path outside its head.
  await expect(page.locator('[data-testid="exit-plan-text"]')).toContainText('再对照计划逐条检查')
  const text = await main.innerText()
  expect(text).not.toContain('exit_plan_mode')
  expect(text).not.toContain('act-e2e-exitplan-idle')
  // The four choices, in the overlay's order. The feedback field waits for
  // its own verdict: it appears only after「继续规划」is chosen, and its send
  // stays disabled until there are words to send.
  await expect(page.locator('[data-testid="exit-plan-approve-clear"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-approve"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-keep-planning"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-ask-review"]')).toBeVisible()
  await expect(dock.locator('[data-testid="exit-plan-feedback"]')).toHaveCount(0)
  await page.locator('[data-testid="exit-plan-keep-planning"]').click()
  await expect(dock.locator('[data-testid="exit-plan-feedback"] input')).toHaveAttribute('placeholder', '告诉规划模型要改什么')
  await expect(dock.locator('[data-testid="exit-plan-send-feedback"]')).toBeDisabled()
  await shot(page, 'exit-plan-approval-dock')
})

test('a review in flight is an event away: the card says so and the reviewer has its own view', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-exit-plan')
  const { runId, actionId } = parkExitPlan(session, 'running')
  const now = Math.floor(Date.now() / 1000)
  const reviewId = 'plan-review:e2e-running'
  sqlite(`
    ${event(session, runId, 'e2e-exitplan-started', 'plan_review_started', {
      action_id: actionId, review_id: reviewId,
      provider: 'deepseek', model: 'deepseek-v4-flash',
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

  // The timeline's plan-reviewer card is the way into the reviewer's own tab
  // in the drawer beside the conversation, where its tool cards live — nothing
  // about the review leaks into the conversation as text.
  const reviewCard = page.locator('[data-testid="subagent-call-card"]')
  await expect(reviewCard.first()).toContainText('1 个 plan-reviewer 任务')
  const rows = page.locator('[data-testid="subagent-task-row"]')
  await rows.nth(0).click()
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()
  await expect(drawer).toContainText('review the plan against the README')
  // The reviewer's own calls are its cards here, head worded the engine's way
  // (136): the read it is running reads 正在读取 in a Chinese interface.
  await expect(drawer).toContainText('正在读取')
  await shot(page, 'exit-plan-reviewer-view')
})

test('asking for a review hands it to the planner: the card closes silently', async ({ page }) => {
  test.info().setTimeout(240_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-exit-plan')
  parkExitPlan(session, 'live')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const card = page.locator('[data-testid="exit-plan-approval"]')
  await expect(card).toBeVisible()

  // Ask the configured model to review; the request itself must name the
  // pending approval it is about, and the row the click picked — two rows of
  // one provider/model are two reviewers, and the id is what tells them apart.
  await page.locator('[data-testid="exit-plan-ask-review"]').click()
  const model = page.locator('[data-testid="plan-review-model"]').first()
  await expect(model).toBeVisible()
  // The row shows where its requests go, so twin rows are telling apart.
  await expect(model).toContainText('http')
  const request = page.waitForRequest((req) => req.url().includes('/plan-review') && req.method() === 'POST')
  await model.click()
  const reviewRequest = await request
  expect(reviewRequest.url()).toContain('act-e2e-exitplan-live')
  expect(await reviewRequest.postDataJSON()).toMatchObject({ model: expect.any(String), model_id: expect.any(String) })

  // The review runs as its own subagent on the conversation's timeline…
  await expect(page.locator('[data-testid="subagent-call-card"]').first()).toBeVisible({ timeout: 120_000 })

  // …and a finished review is delivered, not shown for a second decision: the
  // handoff is internal plumbing, so the card the review was asked from
  // leaves with no line behind it. (The revised plan comes back as a new
  // approval of its own.)
  await expect(card).toHaveCount(0, { timeout: 180_000 })
  await shot(page, 'exit-plan-approval-delivered')
})

test('handing the plan to another model approves with the row on the wire and the tech-lead contract in the tool result', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  // The launch project is a git repository and the config holds other models,
  // so the gate offers the hand-off for real (plan 283).
  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-exit-plan')
  parkExitPlan(session, 'handoff')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const dock = page.locator('[data-testid="exit-plan-approval"]')
  await expect(dock).toBeVisible()
  const handOff = page.locator('[data-testid="exit-plan-hand-off"]')
  await expect(handOff).toBeVisible()
  await expect(handOff).toHaveText('交给其他模型执行')

  // The picker is the review list wearing its second caption: the grey line
  // says where the implementation runs, and every row names its endpoint.
  await handOff.click()
  await expect(page.locator('.xp-pick .cap')).toHaveText('交给哪个模型执行？')
  await expect(page.locator('.xp-pick .hint')).toHaveText('在新的 worktree 里实施，不动当前分支')
  const rows = page.locator('[data-testid="exit-plan-hand-off-model"]')
  await expect(rows.nth(1)).toContainText('http')

  const approve = page.waitForRequest((req) => req.url().includes('/approve') && req.method() === 'POST')
  await rows.nth(1).click()
  const body = await (await approve).postDataJSON()
  // The engine hands off by configured row id — two rows of one provider/model
  // are two executors — never without one.
  expect(body?.hand_off_model).toMatchObject({ id: expect.any(String), provider: expect.any(String), model: expect.any(String) })
  await expect(dock).toHaveCount(0, { timeout: 30_000 })
  await shot(page, 'exit-plan-approval-handed-off')

  // The decision is durable: the answer the engine stores is what a replay
  // reads the hand-off back from (the parked seed has no live run to write a
  // tool result; the tech-lead contract's own text is pinned by unit goldens).
  const answer = execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(),
    `SELECT answer_json FROM fb_actions WHERE id='act-e2e-exitplan-handoff'`], { encoding: 'utf8' })
  expect(answer).toContain('hand_off_model')
  expect(answer).toMatch(/"id":"llm-[^"]+"/)
  expect(answer).toContain('"provider"')
  expect(answer).toContain('"model"')
})

test('a review handed back is asked again in its own conversation: the continuation names the first review', async ({ page }) => {
  test.info().setTimeout(300_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-exit-plan')
  seedPlan(session, '# 计划：交接后的复审\n\n1. 先读 README。\n2. 再对照计划逐条检查。\n')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  // Plan mode is the person's own switch; the gate only exists while it is on.
  await page.click('[data-testid="mode-chip"]')
  await page.click('[data-testid="mode-option-plan"]')
  await page.fill('textarea', `[[e2e:tool-call]]${JSON.stringify({ name: 'exit_plan_mode', arguments: {} })}\n\n计划写好了，退出计划模式。`)
  await page.press('textarea', 'Enter')
  const dock = page.locator('[data-testid="exit-plan-approval"]')
  await expect(dock).toBeVisible({ timeout: 60_000 })
  // Nothing to continue yet: the again row is absent (plan 283).
  await expect(page.locator('[data-testid="exit-plan-review-again"]')).toHaveCount(0)

  // One real review, picked from the list.
  await page.click('[data-testid="exit-plan-ask-review"]')
  const reviewer = page.locator('[data-testid="plan-review-model"]').first()
  await expect(reviewer).toBeVisible()
  await reviewer.click()
  // The review runs as its own subagent and finishes into the planner; the
  // dock leaves until the planner asks again.
  await expect(dock).toHaveCount(0, { timeout: 180_000 })
  const firstStarted = execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(),
    `SELECT json_extract(payload_json,'$.review_id') FROM fb_session_events WHERE session_id='${session}' AND event_type='plan_review_started' ORDER BY rowid DESC LIMIT 1`], { encoding: 'utf8' }).trim()
  expect(firstStarted).toMatch(/^plan-review:/)

  // The planner's revised exit call: parked the same way a real one parks.
  const now = Math.floor(Date.now() / 1000)
  const actionId = `act-e2e-again-${Date.now()}`
  const runId = `run-e2e-again-${Date.now()}`
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('${runId}', '${session}', 'plan the change', 'waiting_action', ${now - 60}, ${now - 60});
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
      VALUES('${actionId}', '${session}', 'exit_plan_mode', 'pending', '${sqlJson({ session_id: session })}', ${now - 60}, ${now - 60});
    INSERT INTO fb_run_waits(run_id, action_id, tool_name, tool_input_json, created_at, updated_at)
      VALUES('${runId}', '${actionId}', 'exit_plan_mode', '{}', ${now - 60}, ${now - 60});
  `)

  // The dock is back and the again row is there, naming the reviewer that
  // ran the first review (plan 283). The seed landed after the page had the
  // session open, so the initial load — the read every parked approval rides
  // — picks it up.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(dock).toBeVisible({ timeout: 60_000 })
  const again = page.locator('[data-testid="exit-plan-review-again"]')
  await expect(again).toBeVisible()
  await expect(again).toContainText('再评审')

  const review = page.waitForRequest((req) => req.url().includes('/plan-review') && req.method() === 'POST')
  await again.click()
  const reviewBody = await (await review).postDataJSON()
  // The gate — not the request — knows which review to continue, so the body
  // carries nothing but the flag.
  expect(reviewBody).toEqual({ again: true })
  // No second picker: the reviewer is the one the user already picked.
  await expect(page.locator('.xp-pick')).toHaveCount(0)

  // The continuation is the SAME reviewer: the second started event points
  // back at the first, and the spawned subagent keeps its identity. The card
  // is drawn from the started event, so the spawned row can trail it by a
  // moment — poll until both rounds' subagents are on record.
  await expect(page.locator('[data-testid="subagent-call-card"]').nth(1)).toBeVisible({ timeout: 120_000 })
  let chain = ''
  for (let attempt = 0; attempt < 50; attempt++) {
    chain = execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(),
      `SELECT json_extract(payload_json,'$.continues_review_id'), json_extract(payload_json,'$.review_id') FROM fb_session_events WHERE session_id='${session}' AND event_type='plan_review_started' ORDER BY rowid DESC LIMIT 1; ` +
      `SELECT json_extract(payload_json,'$.agent_id'), json_extract(payload_json,'$.worker_session_id') FROM fb_session_events WHERE session_id='${session}' AND event_type='subagent_spawned' ORDER BY rowid;`], { encoding: 'utf8' })
    if (chain.trim().split('\n').length >= 3) break
    await page.waitForTimeout(200)
  }
  const [continues, second, agent1, worker1, agent2, worker2] = chain.trim().split('\n').flatMap((l) => l.split('|'))
  expect(continues).toBe(firstStarted)
  expect(second).toMatch(/^plan-review:/)
  expect(agent2).toBe(agent1)
  expect(worker2).toBe(worker1)
  await shot(page, 'exit-plan-review-again')
})

test('approving with the context cleared says so on the wire and brings the input back', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-exit-plan')
  parkExitPlan(session, 'approve')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="exit-plan-approval"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-approve-clear"]')).toBeVisible()

  const approve = page.waitForRequest((req) => req.url().includes('/approve') && req.method() === 'POST')
  await page.locator('[data-testid="exit-plan-approve-clear"]').click()
  const approveBody = await (await approve).postDataJSON()
  expect(approveBody?.clear_context).toBe(true)
  // The decision made, the input is back where the dock stood.
  await expect(page.locator('[data-testid="exit-plan-approval"]')).toHaveCount(0, { timeout: 30_000 })
  await expect(page.locator('.chat-composer textarea')).toBeVisible()
  await shot(page, 'exit-plan-approval-approved')
})
