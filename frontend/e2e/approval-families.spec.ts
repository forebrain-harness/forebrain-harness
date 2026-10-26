import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

import { seedSessionInRoot, signIn, shot } from './support'

/**
 * Every family an approval can come from, proven on the real page against the
 * real gateway: each parked action renders as the one card that says what is
 * about to happen (its own question), the facts this decision turns on, and
 * the engine's own decision options — never the tool's internal name, an
 * action id, or the engine-word buttons the old generic card printed.
 *
 * The actions are seeded straight into the gateway's state database in the
 * exact shapes the tools' approval payloads take, because the suggestion and
 * the decision options the page reads are computed by the gateway from those
 * payloads, not scripted here.
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

interface ParkedAction {
  id: string
  kind: string
  payload: Record<string, unknown>
}

/** Park a session's run on one approval per family, as the tools' own hooks
 * would have written them. Returns the ids the page will read. */
function parkApprovals(session: string, actions: ParkedAction[]): void {
  const now = Math.floor(Date.now() / 1000)
  const rows = actions.map((action, i) => `
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
      VALUES('${action.id}', '${session}', '${action.kind}', 'pending', '${sqlJson(action.payload)}', ${now - 60 + i}, ${now - 60 + i});`)
  sqlite(rows.join('\n'))
}

test('every approval family asks its own question, with its own facts and decisions', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-approval-families')
  parkApprovals(session, [
    {
      id: 'act-e2e-write-over', kind: 'write_file',
      payload: { file_path: 'frontend/src/lib/api.ts', resolved_file_path: '/w/frontend/src/lib/api.ts', content: 'x\n'.repeat(96), existing_lines: 41 },
    },
    {
      id: 'act-e2e-write-new', kind: 'write_file',
      payload: { file_path: 'cmd/forebrain/main.go', resolved_file_path: '/w/cmd/forebrain/main.go', content: 'x\n'.repeat(96) },
    },
    {
      id: 'act-e2e-shell-danger', kind: 'shell',
      payload: {
        command: 'rm -rf build/ && go build -o ./build/bin/forebrain ./cmd/forebrain',
        command_safety: { origin: 'shell', known_safe: false, used_complex_parsing: false },
        approval_reason: 'dangerous_shell_syntax', force_tool_approval: true, dangerous_rule: 'forced_rm',
      },
    },
    {
      id: 'act-e2e-shell-unknown', kind: 'shell',
      payload: { command: 'mvn clean verify', timeout_ms: 10000, approval_reason: 'unknown_shell_command' },
    },
    {
      id: 'act-e2e-network', kind: 'shell',
      payload: {
        command: 'gh issue list --repo unionj-cloud/forebrain-harness',
        approval_reason: 'network_denied', sandbox_denial_reason: 'network access is denied by policy',
        network_approval_context: { host: 'api.github.com', protocol: 'https' }, network_port: 443,
      },
    },
    {
      id: 'act-e2e-sandbox', kind: 'shell',
      payload: {
        command: 'cp ./build/bin/forebrain ~/.local/bin/forebrain',
        sandbox_permissions: 'require_escalated', approval_reason: 'sandbox_denied',
        sandbox_denial_reason: 'write outside project roots', force_tool_approval: true,
      },
    },
    {
      id: 'act-e2e-sandbox-scoped', kind: 'shell',
      payload: {
        command: 'cp ./build/bin/forebrain /tmp/out/forebrain',
        approval_reason: 'sandbox_denied', scoped_sandbox_retry: true,
        additional_permissions: { file_system: { entries: [{ path: '/tmp/out', access: 'write' }] } },
      },
    },
    {
      id: 'act-e2e-permissions', kind: 'request_permissions',
      payload: {
        request_permissions: true, reason: 'run the acceptance suite', cwd: '/w',
        permissions: {
          file_system: {
            entries: [
              { path: { type: 'path', path: '~/notes' }, access: 'read' },
              { path: { type: 'path', path: '/tmp/out' }, access: 'write' },
            ],
            glob_scan_max_depth: 4,
          },
          network: { enabled: true },
        },
      },
    },
    {
      id: 'act-e2e-mcp', kind: 'mcp__github__create_issue',
      payload: { repo: 'unionj-cloud/forebrain-harness', title: 'cookie login still 401' },
    },
  ])

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const card = (id: string) => page.locator(`[data-testid="pending-approval"][data-action-id="${id}"]`)
  const facts = (id: string) => card(id).locator('[data-testid="approval-gate-fact"]')
  const decisions = (id: string) => card(id).locator('[data-testid^="pending-decision-"]')

  // ① An overwrite says what it replaces; a new file says its size.
  await expect(card('act-e2e-write-over')).toContainText('要覆盖这个文件吗？')
  await expect(facts('act-e2e-write-over')).toHaveText(['+96 −41 行'])
  await expect(decisions('act-e2e-write-over')).toHaveText(['覆盖', '本会话不再问', '不要写'])
  await expect(card('act-e2e-write-new')).toContainText('要新建这个文件吗？')
  await expect(facts('act-e2e-write-new')).toHaveText(['96 行'])
  await expect(decisions('act-e2e-write-new').first()).toHaveText('写入')

  // ② A dangerous command says what the engine concluded about it; an unknown
  // one asks without a fact line, because no rule names what it does.
  await expect(card('act-e2e-shell-danger')).toContainText('要运行这条命令吗？')
  await expect(facts('act-e2e-shell-danger')).toHaveText(['强制删除文件（rm -f），引擎把这类命令列为危险命令'])
  await expect(card('act-e2e-shell-unknown')).toContainText('要运行这条命令吗？')
  await expect(facts('act-e2e-shell-unknown')).toHaveCount(0)

  // ③ A host on the public network: one host line, four answers.
  await expect(card('act-e2e-network')).toContainText('要让它访问外网吗？')
  await expect(facts('act-e2e-network')).toHaveText(['api.github.com · HTTPS · 443'])
  await expect(decisions('act-e2e-network')).toHaveText(['允许', '本会话都允许', '以后都允许', '不要访问'])

  // ④ The sandbox: what it did, and the reason it gave in its own words — the
  // denial can be about the network as readily as about files. A scoped retry
  // says the scope it would gain instead.
  await expect(card('act-e2e-sandbox')).toContainText('沙箱拦下了这条命令，要在沙箱外运行吗？')
  await expect(facts('act-e2e-sandbox')).toHaveText(['沙箱拦下了这条命令', 'write outside project roots'])
  await expect(card('act-e2e-sandbox-scoped')).toContainText('沙箱拦下了这条命令，要在沙箱外运行吗？')
  await expect(facts('act-e2e-sandbox-scoped')).toHaveText(['可写 /tmp/out'])

  // ⑤ A permission request lists what it asks for, one line each, read out of
  // the entry objects the engine marshals — never a stringified object, and the
  // network grant stated as the one thing the wire carries.
  await expect(card('act-e2e-permissions')).toContainText('要给它这些权限吗？')
  await expect(facts('act-e2e-permissions')).toHaveText(['可读 ~/notes', '可写 /tmp/out', '可访问网络'])
  await expect(decisions('act-e2e-permissions')).toHaveText(['本轮允许', '本轮允许，自动审查', '本会话允许', '不授予'])

  // ⑥ An MCP call names its server and tool, and shows what is being sent
  // under labels rather than raw keys.
  await expect(card('act-e2e-mcp')).toContainText('要调用 github 的 create_issue 吗？')
  await expect(facts('act-e2e-mcp').first()).toHaveText('Repo: unionj-cloud/forebrain-harness')
  await expect(decisions('act-e2e-mcp')).toHaveText(['调用', '本会话不再问', '以后都不再问', '不要调用'])

  // Nothing anywhere is the tool's internal name, an action id, the engine-word
  // buttons the old generic card printed, or a stringified permission entry.
  const main = page.locator('.chat-shell main')
  await expect(main).not.toContainText('write_file')
  await expect(main).not.toContainText('mcp__github__create_issue')
  await expect(main).not.toContainText('[object Object]')
  await expect(main).not.toContainText('批准一次')
  await expect(main).not.toContainText('拒绝并中断本轮')
  await shot(page, 'approval-families')
})

/** A stored assistant row that issued the given calls. */
function gateAssistantRow(session: string, runId: string, calls: { id: string; name: string; args: unknown }[], atSec: number): string {
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

/** A stored tool row holding a call open on the reader (the engine's own
 *  awaiting record), so the reload draws the waiting card the live page had.
 *  Its meta carries the call's input, as the engine's rows do — the card head
 *  reads the object of the sentence from there (pkg/tool DisplayHeaderParts). */
function gateAwaitingRow(
  session: string,
  runId: string,
  callId: string,
  toolName: string,
  summary: string,
  input: Record<string, unknown> | undefined,
  atSec: number,
): string {
  const meta = { tool_name: toolName, status: 'awaiting approval', ...(input ? { input } : {}) }
  const parts = [
    { tool_call_id: callId, type: 'tool_result_meta' },
    { type: 'tool_display', summary, body: '', tool_meta_json: JSON.stringify(meta) },
  ]
  return `INSERT INTO fb_messages(session_id, run_id, role, content, parts, tool_step_id, tool_meta_json, created_at)
    VALUES('${session}', '${runId}', 'tool', '${sqlJson({})}', '${sqlJson(parts)}', '${callId}', '${sqlJson(meta)}', ${atSec});`
}

/** One session event, at a moment given in epoch ms. */
function gateEvent(session: string, runId: string, eventId: string, type: string, payload: unknown, atMs: number): string {
  return `INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
    VALUES('${session}', '${runId}', '${eventId}', '${type}', '${sqlJson(payload)}', ${atMs});`
}

/**
 * The waiting gate hangs on the call it holds (plan 138): one card on the
 * activity line — the clock for its node, the facts as its panel, the question
 * and its answers closing it — never a second box at the thread's end. The
 * refusal follows the family: the cancel family opens the words field, the
 * decline family acts at once; written words refuse this one call and settle
 * the card on its own words.
 */
test('a waiting gate hangs on its own call, and the refusal follows the family', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedSessionInRoot(page, process.env.E2E_PROJECT_PARENT!, 'e2e-approval-gated')
  const now = Math.floor(Date.now() / 1000)
  const runId = 'run-e2e-gated'
  const rmAction = 'act-e2e-gated-rm'
  const permsAction = 'act-e2e-gated-perms'
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, created_at, updated_at)
      VALUES ('${runId}', '${session}', 'clean the build', 'waiting_action', ${now - 60}, ${now - 60});
    INSERT INTO fb_messages(session_id, run_id, role, content, created_at)
      VALUES('${session}', '${runId}', 'user', 'clean the build', ${now - 60});
    ${gateAssistantRow(session, runId, [
      { id: 'call-rm', name: 'shell', args: { command: 'rm -rf build/ && go build ./...' } },
      { id: 'call-perms', name: 'request_permissions', args: { request_permissions: true } },
    ], now - 55)}
    ${gateAwaitingRow(session, runId, 'call-rm', 'shell', 'running rm -rf build/ && go build ./...', { command: 'rm -rf build/ && go build ./...' }, now - 50)}
    ${gateAwaitingRow(session, runId, 'call-perms', 'request_permissions', 'requesting permissions', { request_permissions: true }, now - 49)}
    ${gateEvent(session, runId, 'e2e-rm-start', 'tool_call_started', {
      step_id: 'call-rm', tool_name: 'shell',
      header_parts: { action: 'Running', target: 'rm -rf build/ && go build ./...' },
      tool_meta: { tool_name: 'shell', status: 'awaiting approval', input: { command: 'rm -rf build/ && go build ./...' } },
    }, (now - 50) * 1000)}
    ${gateEvent(session, runId, 'e2e-perms-start', 'tool_call_started', {
      step_id: 'call-perms', tool_name: 'request_permissions',
      header_parts: { action: 'Requesting permissions' },
      tool_meta: { tool_name: 'request_permissions', status: 'awaiting approval', input: { request_permissions: true } },
    }, (now - 49) * 1000)}
    ${gateEvent(session, runId, 'e2e-rm-gate', 'approval_requested', {
      action_id: rmAction, action_kind: 'shell', tool_step_id: 'call-rm',
      message: 'Awaiting approval or authorization to continue',
    }, (now - 48) * 1000)}
    ${gateEvent(session, runId, 'e2e-perms-gate', 'approval_requested', {
      action_id: permsAction, action_kind: 'request_permissions', tool_step_id: 'call-perms',
      message: 'Awaiting approval or authorization to continue',
    }, (now - 47) * 1000)}
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
      VALUES('${rmAction}', '${session}', 'shell', 'pending', '${sqlJson({
        command: 'rm -rf build/ && go build ./...', approval_reason: 'dangerous_shell_syntax',
        force_tool_approval: true, dangerous_rule: 'forced_rm',
      })}', ${now - 48}, ${now - 48});
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
      VALUES('${permsAction}', '${session}', 'request_permissions', 'pending', '${sqlJson({
        request_permissions: true, reason: 'run the acceptance suite',
        permissions: { file_system: { entries: [{ path: { type: 'path', path: '/tmp/out' }, access: 'write' }] } },
      })}', ${now - 47}, ${now - 47});
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })

  // The gate is the call's own card: the footer lives inside the tool card,
  // and nothing waits at the thread's end.
  const rmCard = page.locator('.tool-card').filter({ has: page.locator('[data-testid="approval-gate-shell"]') })
  await expect(rmCard).toBeVisible()
  await expect(page.locator('[data-testid="pending-approval"]')).toHaveCount(0)
  // The node says the call is waiting on the reader — the clock, named for a
  // reader who cannot see it: both waiting calls' nodes say 等待批准, never the
  // sentence the head already speaks.
  await expect(page.locator('.activity-step .tool-status--awaiting svg')).toHaveCount(2)
  await expect(page.locator('.activity-step .tool-status--awaiting')).toContainText(['等待批准', '等待批准'])
  await expect(rmCard.locator('.ttl')).toContainText('正在运行')
  await expect(rmCard.locator('.ttl')).toContainText('rm -rf build/ && go build ./...')
  // The panel carries what the decision turns on; the footer asks.
  await expect(rmCard.locator('[data-testid="approval-gate-fact"]')).toHaveText([
    '强制删除文件（rm -f），引擎把这类命令列为危险命令',
  ])
  await expect(rmCard).toContainText('要运行这条命令吗？')

  // The cancel family's refusal opens the words field, focused, with the send
  // naming what an empty send does.
  const refusal = rmCard.locator('[data-testid="pending-decision-cancel"]')
  await expect(refusal).toHaveText('不要运行')
  await refusal.click()
  const field = rmCard.locator('[data-testid="gate-feedback-input"]')
  await expect(field).toBeFocused()
  await expect(field).toHaveAttribute('placeholder', '告诉它该怎么做（可不填）')
  await expect(rmCard.locator('[data-testid="gate-feedback-send"]')).toHaveText('拒绝并停下')

  // Written words change the send's name and its verdict: only this call is
  // refused, and the words reach the model as the user's own.
  await field.fill('改成移到 trash/')
  await expect(rmCard.locator('[data-testid="gate-feedback-send"]')).toHaveText('发送')
  const deny = page.waitForRequest((req) => req.url().includes(`/actions/${rmAction}/approve`) && req.method() === 'POST')
  await rmCard.locator('[data-testid="gate-feedback-send"]').click()
  expect(await (await deny).postDataJSON()).toEqual({ decision: 'deny', reason: '改成移到 trash/' })

  // The settled refusal is one sentence plus the user's own words, on the card
  // the gate hung on. A reload draws a finished call from its transcript row
  // (a historical tool_call_completed replays nothing: useChatStream treats it
  // as transcript-backed), so the seeded run records the denial the way the
  // engine's own completion row carries it: the row's meta turns denied and
  // keeps the user's words (pkg/run toolDisplayState → BuildToolMeta).
  const deniedMeta = {
    tool_name: 'shell',
    status: 'denied',
    deny_feedback: '改成移到 trash/',
    input: { command: 'rm -rf build/ && go build ./...' },
  }
  sqlite(`
    UPDATE fb_messages
       SET tool_meta_json = '${sqlJson(deniedMeta)}',
           parts = '${sqlJson([
             { tool_call_id: 'call-rm', type: 'tool_result_meta' },
             { type: 'tool_display', summary: 'running rm -rf build/ && go build ./...', body: '', tool_meta_json: JSON.stringify(deniedMeta) },
           ])}'
     WHERE session_id = '${session}' AND tool_step_id = 'call-rm';
  `)
  await page.reload({ waitUntil: 'networkidle' })
  // A settled card reloads folded (the conversation's default), so it is found
  // by its head and opened the way a reader opens any finished call; the
  // refusal body is what opening it shows.
  const settled = page.locator('.tool-card').filter({ hasText: /已拒绝\s*rm -rf build\// })
  await expect(settled).toBeVisible()
  await settled.locator('.tool-head').click()
  await expect(settled.locator('[data-testid="tool-refused"]')).toBeVisible()
  await expect(settled.locator('[data-testid="tool-refused"] .say')).toHaveText('你拒绝了这条命令，它没有运行。')
  await expect(settled.locator('[data-testid="tool-refused"] .said')).toHaveText('改成移到 trash/')
  await expect(settled.locator('[data-testid="approval-gate-shell"]')).toHaveCount(0)

  // The decline family acts at once: no field ever opens, the decision goes
  // straight out, and the turn carries on.
  const permsCard = page.locator('.tool-card').filter({ has: page.locator('[data-testid="approval-gate-request_permissions"]') })
  await expect(permsCard).toBeVisible()
  const decline = page.waitForRequest((req) => req.url().includes(`/actions/${permsAction}/approve`) && req.method() === 'POST')
  await permsCard.locator('[data-testid="pending-decision-decline"]').click()
  expect(await (await decline).postDataJSON()).toEqual({ decision: 'decline', reason: 'web:decline' })
  await expect(permsCard.locator('[data-testid="gate-feedback"]')).toHaveCount(0)
  await shot(page, 'approval-gated-card')
})
