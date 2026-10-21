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
