import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

import { signIn, shot } from './support'

/**
 * The subagent_* call cards, proven on the real page: one card per call, one
 * row per task, the rows' own clocks, and none of the call's raw material —
 * no JSON, no task id, no status=, no tool-use tally.
 *
 * The calls are seeded straight into the gateway's state database because the
 * acceptance harness runs with subagents disabled (agents.defaults is absent
 * in its yaml), so no model — fake or real — can be offered the tools. The
 * seeded transcript is the same shape a real send → status → list → wait →
 * close sequence writes, and the page renders it through the same reload path
 * a real session takes.
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

/** A stored tool row answering one call; content is the JSON the model saw. */
function toolRow(session: string, runId: string, callId: string, toolName: string, content: unknown, atSec: number): string {
  const parts = [{ tool_call_id: callId, type: 'tool_result_meta' }]
  return `INSERT INTO fb_messages(session_id, run_id, role, content, parts, tool_step_id, tool_meta_json, created_at)
    VALUES('${session}', '${runId}', 'tool', '${sqlJson(content)}', '${sqlJson(parts)}', '${callId}',
      '${sqlJson({ tool_name: toolName, status: 'completed' })}', ${atSec});`
}

/** One session event, at a moment given in epoch ms. */
function event(session: string, runId: string, eventId: string, type: string, payload: unknown, atMs: number): string {
  return `INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
    VALUES('${session}', '${runId}', '${eventId}', '${type}', '${sqlJson(payload)}', ${atMs});`
}

test('a fanout card carries its tasks, their clocks, and nothing raw', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)
  const now = Math.floor(Date.now() / 1000)

  const fanoutArgs = {
    max_parallel: 2,
    tasks: [
      { title: '任务16 migrate 导入', prompt: 'run the migration', subagent_type: 'explore' },
      { title: '任务17 扩展目录', prompt: 'expand the catalog', subagent_type: 'explore' },
      { title: '', prompt: '', subagent_type: 'explore' },
    ],
  }
  const fanoutResult = {
    summary: { total: 3, succeed: 1, failed: 0, finished: now - 10 },
    results: [
      { index: 0, task: 'run the migration', subagent_type: 'explore', output: 'done', ok: true },
      { index: 2, task: '', subagent_type: 'explore', ok: false, error: 'skipped: empty prompt' },
    ],
  }
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-fanout', '${session}', 'dispatch the work', 'done', ${now - 60}, ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', 'run-e2e-fanout', 'user', 'dispatch the work', ${now - 60});
    ${assistantRow(session, 'run-e2e-fanout', [{ id: 'call-e2e-fanout', name: 'subagent_fanout', args: fanoutArgs }], now - 50)}
    ${toolRow(session, 'run-e2e-fanout', 'call-e2e-fanout', 'subagent_fanout', fanoutResult, now - 9)}
    ${event(session, 'run-e2e-fanout', 'e2e-spawn-a', 'subagent_spawned', {
      agent_id: 'subagent-e2e-a', agent_type: 'explore', task_id: 'subagent-e2e-a',
      title: '任务16 migrate 导入', task: 'run the migration', execution_id: 'exec-e2e-a',
      parent_run_id: 'run-e2e-fanout', parent_tool_call_id: 'call-e2e-fanout', task_index: 0,
    }, (now - 30) * 1000)}
    ${event(session, 'run-e2e-fanout', 'e2e-end-a', 'subagent_ended', {
      agent_id: 'subagent-e2e-a', agent_type: 'explore', task_id: 'subagent-e2e-a',
      status: 'ok', execution_id: 'exec-e2e-a', parent_run_id: 'run-e2e-fanout',
      parent_tool_call_id: 'call-e2e-fanout', task_index: 0, finished_at_ms: (now - 10) * 1000,
    }, (now - 10) * 1000)}
    ${event(session, 'run-e2e-fanout', 'e2e-spawn-b', 'subagent_spawned', {
      agent_id: 'subagent-e2e-b', agent_type: 'explore', task_id: 'subagent-e2e-b',
      title: '任务17 扩展目录', task: 'expand the catalog', execution_id: 'exec-e2e-b',
      parent_run_id: 'run-e2e-fanout', parent_tool_call_id: 'call-e2e-fanout', task_index: 1,
    }, (now - 5) * 1000)}
    ${event(session, 'run-e2e-fanout', 'e2e-tool-b', 'tool_call_started', {
      step_id: 'call-e2e-tool-b', tool_name: 'read', summary: 'reading the catalog',
      tool_meta: { tool_name: 'read', status: 'running', agent_id: 'subagent-e2e-b', invocation: 'read pkg/catalog/dir.go' },
    }, (now - 4) * 1000)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-testid="subagent-call-card"]')

  const body = page.locator('.chat-shell main')
  // The header counts the tasks and their type; the card never shows the
  // call's arguments or its result document.
  await expect(body).toContainText('正在运行 3 个 explore 任务')
  // One row per task: the ended one with its final elapsed, the running one
  // with a walking clock, the skipped one with its reason.
  const rows = page.locator('[data-testid="subagent-task-row"]')
  await expect(rows).toHaveCount(3)
  await expect(rows.nth(0)).toContainText('任务16 migrate 导入')
  await expect(rows.nth(0).locator('[data-testid="subagent-row-clock"]')).toHaveText(' · 20s')
  await expect(rows.nth(1)).toContainText('任务17 扩展目录')
  const walking = rows.nth(1).locator('[data-testid="subagent-row-clock"]')
  const startedAt = await walking.innerText()
  await page.waitForTimeout(3_500)
  const walked = await walking.innerText()
  expect(secondsOf(startedAt)).toBeLessThanOrEqual(secondsOf(walked) - 3)
  // The skipped row shows the marker and the reason, never a checkmark or a
  // success count that includes it.
  await expect(rows.nth(2)).toContainText('（空任务）')
  await expect(rows.nth(2)).toContainText('skipped: empty prompt')
  // The running task's latest tool is named once — no separate tally line.
  await expect(rows.nth(1)).toContainText('最近：read pkg/catalog/dir.go')
  const text = await body.innerText()
  expect(text).not.toContain('{"')
  expect(text).not.toContain('status=')
  expect(text).not.toMatch(/^\s*\+?\d*\s*tool uses?\s*$/m)
  await shot(page, 'subagent-cards-fanout')

  // The row is the way into that subagent's own view.
  await rows.nth(0).click()
  await expect(page.getByRole('button', { name: '返回' })).toBeVisible()
  await expect(page.locator('.chat-shell main')).toContainText('run the migration')
  await shot(page, 'subagent-cards-view')
  // Back to the conversation before the reload; the agent view would
  // otherwise come back restored and keep the conversation hidden. The wait
  // covers the view state's debounced persistence.
  await page.getByRole('button', { name: '返回' }).click()
  await expect(page.locator('[data-testid="subagent-call-card"]').first()).toBeVisible()
  await page.waitForTimeout(300)

  // A reload draws the same cards from the stored rows and events.
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-testid="subagent-call-card"]')
  await expect(page.locator('.chat-shell main')).toContainText('正在运行 3 个 explore 任务')
  await expect(page.locator('[data-testid="subagent-task-row"]')).toHaveCount(3)
  await shot(page, 'subagent-cards-reloaded')
})

test('an empty list is a card that says it listed nothing', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)
  const now = Math.floor(Date.now() / 1000)
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-list', '${session}', 'list them', 'done', ${now - 30}, ${now - 30});
    ${assistantRow(session, 'run-e2e-list', [{ id: 'call-e2e-list', name: 'subagent_list', args: {} }], now - 20)}
    ${toolRow(session, 'run-e2e-list', 'call-e2e-list', 'subagent_list', { records: [] }, now - 19)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-testid="subagent-call-card"]')
  const main = page.locator('.chat-shell main')
  await expect(main).toContainText('已列出 0 个任务')
  await expect(main).toContainText('（无输出）')
  const text = await main.innerText()
  expect(text).not.toContain('{"')
  expect(text).not.toContain('status=')
})

test('the five lifecycle verbs each draw their own card, none with raw material', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)
  const now = Math.floor(Date.now() / 1000)
  const record = (status: string, extra: Record<string, unknown> = {}) => ({
    agent_id: 'subagent-e2e-a', agent_kind: 'typed', agent_type: 'general-purpose',
    task_id: 'subagent-e2e-a', title: '计划001 Go车道实施', task: 'the whole dispatch prompt',
    status, started_at: now - 60, execution_id: 'exec-e2e-a', run_id: 'exec-e2e-a',
    worker_session_id: 'worker-1', session_id: session, ...extra,
  })
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('run-e2e-five', '${session}', 'drive the lifecycle', 'done', ${now - 90}, ${now - 90});
    ${assistantRow(session, 'run-e2e-five', [
      { id: 'call-e2e-send', name: 'subagent_send', args: { title: '计划001 Go车道实施', task: 'the whole dispatch prompt', subagent_type: 'general-purpose' } },
      { id: 'call-e2e-status', name: 'subagent_status', args: { agent_id: 'subagent-e2e-a' } },
      { id: 'call-e2e-wait', name: 'subagent_wait', args: { agent_id: 'subagent-e2e-a', timeout_seconds: 1 } },
      { id: 'call-e2e-close', name: 'subagent_close', args: { agent_id: 'subagent-e2e-a' } },
      { id: 'call-e2e-list', name: 'subagent_list', args: {} },
    ], now - 80)}
    ${toolRow(session, 'run-e2e-five', 'call-e2e-send', 'subagent_send', record('done', { finished_at: now - 10 }), now - 70)}
    ${toolRow(session, 'run-e2e-five', 'call-e2e-status', 'subagent_status', record('running'), now - 60)}
    ${toolRow(session, 'run-e2e-five', 'call-e2e-wait', 'subagent_wait', { timed_out: true, record: record('running') }, now - 50)}
    ${toolRow(session, 'run-e2e-five', 'call-e2e-close', 'subagent_close', { stop_requested: true, record: record('running') }, now - 40)}
    ${toolRow(session, 'run-e2e-five', 'call-e2e-list', 'subagent_list', { records: [record('done', { finished_at: now - 10 })] }, now - 30)}
    ${event(session, 'run-e2e-five', 'e2e-five-spawn', 'subagent_spawned', {
      agent_id: 'subagent-e2e-a', agent_type: 'general-purpose', task_id: 'subagent-e2e-a',
      title: '计划001 Go车道实施', task: 'the whole dispatch prompt', execution_id: 'exec-e2e-a',
      parent_run_id: 'run-e2e-five', parent_tool_call_id: 'call-e2e-send', task_index: 0,
    }, (now - 60) * 1000)}
    ${event(session, 'run-e2e-five', 'e2e-five-end', 'subagent_ended', {
      agent_id: 'subagent-e2e-a', agent_type: 'general-purpose', task_id: 'subagent-e2e-a',
      status: 'ok', execution_id: 'exec-e2e-a', parent_run_id: 'run-e2e-five',
      parent_tool_call_id: 'call-e2e-send', task_index: 0, finished_at_ms: (now - 10) * 1000,
    }, (now - 10) * 1000)}
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-testid="subagent-call-card"]')
  const main = page.locator('.chat-shell main')
  await expect(main).toContainText('已在后台运行 1 个 general-purpose 任务')
  await expect(main).toContainText('已查看 1 个 general-purpose 任务')
  await expect(main).toContainText('已等待 1 个 general-purpose 任务')
  await expect(main).toContainText('已停止 1 个 general-purpose 任务')
  await expect(main).toContainText('已列出 1 个任务')
  // A wait that timed out says the agent was still running, and a close that
  // asked a running agent to stop says so.
  await expect(main).toContainText('等待结束时仍在运行')
  await expect(main).toContainText('已请求停止')
  const cards = page.locator('[data-testid="subagent-call-card"]')
  await expect(cards).toHaveCount(5)
  const text = await main.innerText()
  for (const card of await cards.allInnerTexts()) {
    expect(card).not.toContain('{')
    expect(card).not.toContain('status=')
    expect(card).not.toContain('subagent-e2e')
  }
  expect(text).not.toContain('the whole dispatch prompt')
  await shot(page, 'subagent-cards-five')
})

/** Seconds out of a row clock's " · 20s"-shaped label. */
function secondsOf(clock: string): number {
  const match = /(\d+)h (?:(\d+)m )?(?:(\d+)s)?|(\d+)m (?:(\d+)s)?|(\d+)s/.exec(clock.trim())
  if (!match) throw new Error(`not a clock: ${clock}`)
  if (match[1]) return Number(match[1]) * 3600 + Number(match[2] ?? 0) * 60 + Number(match[3] ?? 0)
  if (match[4]) return Number(match[4]) * 60 + Number(match[5] ?? 0)
  return Number(match[6])
}
