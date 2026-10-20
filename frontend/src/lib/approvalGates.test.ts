import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { setLocale } from '@/locales'
import type { PermissionSuggestionRecord } from '@/lib/approvalSuggestions'
import { approvalFacts, approvalFamily, approvalQuestion, parseApprovalPayload } from './approvalGates'

/**
 * The family a pending approval belongs to decides the question it asks and
 * the facts it shows. These tests pin the engine's evaluation order — a
 * network approval rides on a shell payload, a sandbox-denied retry can be a
 * shell action, a read that asks is the protected read — and every fact
 * string exactly, because the card's copy is the contract: a fact line that
 * drifts says something the engine never said.
 */
describe('approvalGates', () => {
  beforeAll(() => setLocale('zh'))
  afterAll(() => setLocale('en'))

  const networkSuggestion: PermissionSuggestionRecord | null = {
    permissionToolName: 'Bash',
    permissionInput: 'gh issue list',
    networkApprovalContext: { host: 'api.github.com', protocol: 'https' },
    networkPort: 443,
  }

  it('settles families in the engine\'s order, network before sandbox before kind', () => {
    // A network approval arrives on a shell payload that also says the sandbox
    // denied it; the host is the question, so it wins.
    expect(approvalFamily('shell', { approval_reason: 'sandbox_denied' }, networkSuggestion)).toBe('network_host')
    // A sandbox-denied retry is not a plain command, whatever its kind path
    // would say — including the path-scoped variant.
    expect(approvalFamily('shell', { approval_reason: 'sandbox_denied' }, null)).toBe('shell_outside_sandbox')
    expect(approvalFamily('shell', { sandbox_permissions: 'require_escalated' }, null)).toBe('shell_outside_sandbox')
    expect(approvalFamily('shell', { profile_elevation: true }, null)).toBe('shell_outside_sandbox')
    // A read that asks is the protected-path read; an ordinary read never
    // parks the run, so without the flag it is no read approval at all.
    expect(approvalFamily('read_file', { force_tool_approval: true }, null)).toBe('protected_read')
    expect(approvalFamily('read_file', {}, null)).toBe('fallback')
    expect(approvalFamily('write_file', {}, null)).toBe('file_write')
    expect(approvalFamily('edit_file', {}, null)).toBe('file_edit')
    expect(approvalFamily('apply_patch', {}, null)).toBe('apply_patch')
    expect(approvalFamily('request_permissions', {}, null)).toBe('request_permissions')
    expect(approvalFamily('web_search', {}, null)).toBe('web_search')
    expect(approvalFamily('memories_add_ad_hoc_note', {}, null)).toBe('memory_note')
    expect(approvalFamily('mcp__github__create_issue', {}, null)).toBe('mcp_tool')
    expect(approvalFamily('shell', {}, null)).toBe('shell')
    expect(approvalFamily('enter_plan_mode', {}, null)).toBe('enter_plan_mode')
    expect(approvalFamily('exit_plan_mode', {}, null)).toBe('exit_plan_mode')
    expect(approvalFamily('web_fetch', {}, null)).toBe('web_fetch')
    // A question is not an approval; a caller that lands here with one gets
    // the two-choice card, never a card pretending to know more.
    expect(approvalFamily('user_interaction', {}, null)).toBe('fallback')
    expect(approvalFamily('lsp', {}, null)).toBe('fallback')
  })

  it('counts written lines the way every surface does: a trailing newline is not a line', () => {
    expect(approvalFacts('file_write', { content: 'a\nb\n', existing_lines: 0 }, null).facts).toEqual(['2 行'])
    expect(approvalFacts('file_write', { content: '', existing_lines: 0 }, null).facts).toEqual(['0 行'])
  })

  it('says a new file as a count and an overwrite as a delta', () => {
    expect(approvalFacts('file_write', { content: 'x\n'.repeat(96), existing_lines: 0 }, null).facts).toEqual(['96 行'])
    expect(approvalQuestion('file_write', 'write_file', { existing_lines: 0 })).toBe('要新建这个文件吗？')
    expect(approvalFacts('file_write', { content: 'x\n'.repeat(12), existing_lines: 3 }, null).facts).toEqual(['+12 −3 行'])
    expect(approvalQuestion('file_write', 'write_file', { existing_lines: 41 })).toBe('要覆盖这个文件吗？')
    // The diff of a full write is every line added.
    expect(approvalFacts('file_write', { content: 'a\nb' }, null).diff).toEqual([['+', 'a'], ['+', 'b']])
  })

  it('draws an edit as the old lines then the new ones, and never repeats the path', () => {
    const facts = approvalFacts('file_edit', { file_path: 'a.go', old_string: 'one\ntwo', new_string: 'one\nnew\nextra' }, null)
    expect(facts.facts).toEqual(['+3 −2 行'])
    expect(facts.diff).toEqual([['-', 'one'], ['-', 'two'], ['+', 'one'], ['+', 'new'], ['+', 'extra']])
    expect(approvalQuestion('file_edit', 'edit_file', {})).toBe('要改这个文件吗？')
  })

  it('counts a patch from its resolved paths and body, and shows its first hunk verbatim', () => {
    const payload = {
      apply_patch: true,
      resolved_paths: ['/w/pkg/a.go', '/w/pkg/b.go', '/w/frontend/c.ts'],
      patch: '*** Begin Patch\n*** Update File: pkg/a.go\n@@\n ctx\n-old\n+new\n ctx\n*** Update File: pkg/b.go\n@@\n-old2\n+new2\n',
    }
    const facts = approvalFacts('apply_patch', payload, null)
    expect(facts.facts).toEqual(['3 个文件', '+2 −2'])
    expect(facts.diff).toEqual([['', '@@'], ['', ' ctx'], ['-', 'old'], ['+', 'new'], ['', ' ctx']])
    expect(approvalQuestion('apply_patch', 'apply_patch', payload)).toBe('要应用这个补丁吗？')
  })

  it('says what the one dangerous rule does, and says nothing when no rule applies', () => {
    expect(approvalFacts('shell', { command: 'rm -rf build/ && go build ./...' }, null).facts)
      .toEqual(['会强制删掉 build/ 目录下的文件'])
    expect(approvalFacts('shell', { command: 'sudo rm -f /tmp/lock' }, null).facts)
      .toEqual(['会强制删掉 /tmp/lock 目录下的文件'])
    expect(approvalFacts('shell', { command: 'rm -rf' }, null).facts).toEqual(['会强制删除文件'])
    // An unknown command has no rule to quote, and no fact line is invented.
    expect(approvalFacts('shell', { command: 'mvn clean verify', approval_reason: 'unknown_shell_command' }, null).facts).toEqual([])
    expect(approvalQuestion('shell', 'shell', {})).toBe('要运行这条命令吗？')
  })

  it('says the sandbox boundary, or the scope a scoped retry would gain', () => {
    expect(approvalFacts('shell_outside_sandbox', { sandbox_denial_reason: 'write outside roots' }, null).facts)
      .toEqual(['沙箱只允许改这个项目里的文件'])
    expect(approvalFacts('shell_outside_sandbox', {}, null).facts).toEqual(['沙箱拦下了这条命令'])
    const scoped = {
      approval_reason: 'sandbox_denied',
      scoped_sandbox_retry: true,
      additional_permissions: { file_system: { entries: [{ path: '/tmp/out', access: 'write' }] } },
    }
    expect(approvalFacts('shell_outside_sandbox', scoped, null).facts).toEqual(['可写 /tmp/out'])
    expect(approvalQuestion('shell_outside_sandbox', 'shell', {})).toBe('沙箱拦下了这条命令，要在沙箱外运行吗？')
  })

  it('names the host, protocol, and port a network approval is about', () => {
    expect(approvalFacts('network_host', { command: 'gh issue list' }, networkSuggestion).facts)
      .toEqual(['api.github.com · HTTPS · 443'])
    const noPort: PermissionSuggestionRecord | null = { permissionToolName: 'Bash', networkApprovalContext: { host: 'api.github.com', protocol: 'https' } }
    expect(approvalFacts('network_host', {}, noPort).facts).toEqual(['api.github.com · HTTPS'])
    expect(approvalQuestion('network_host', 'shell', {})).toBe('要让它访问外网吗？')
  })

  it('lists what a permission request asks for, one line each', () => {
    const payload = {
      request_permissions: true,
      permissions: {
        file_system: { entries: [{ path: '~/notes', access: 'read' }, { path: '/tmp/out', access: 'write' }] },
        network: { hosts: ['api.example.com'] },
      },
    }
    expect(approvalFacts('request_permissions', payload, null).facts)
      .toEqual(['可读 ~/notes', '可写 /tmp/out', '可连 api.example.com'])
    expect(approvalQuestion('request_permissions', 'request_permissions', payload)).toBe('要给它这些权限吗？')
  })

  it('shows the model\'s own reason for plan mode, and nothing when it wrote none', () => {
    expect(approvalFacts('enter_plan_mode', { reason: '改动跨三个包，先写计划再动手' }, null).facts)
      .toEqual(['改动跨三个包，先写计划再动手'])
    expect(approvalFacts('enter_plan_mode', { reason: '   ' }, null).facts).toEqual([])
    expect(approvalQuestion('enter_plan_mode', 'enter_plan_mode', {})).toBe('要进入计划模式吗？')
  })

  it('shows an MCP call\'s first scalar inputs under the names the model gave them', () => {
    const payload = { repo: 'unionj-cloud/forebrain-harness', title: 'cookie login still 401', body: 'long text', labels: ['bug'], nested: { x: 1 } }
    expect(approvalFacts('mcp_tool', payload, null).facts)
      .toEqual(['repo unionj-cloud/forebrain-harness', 'title cookie login still 401', 'body long text'])
    expect(approvalQuestion('mcp_tool', 'mcp__github__create_issue', payload)).toBe('要调用 github 的 create_issue 吗？')
  })

  it('quotes the search and its cap, falling back to the tool default', () => {
    expect(approvalFacts('web_search', { query: '  forebrain harness  ' }, null).facts).toEqual(['“forebrain harness” · 最多 5 条'])
    expect(approvalFacts('web_search', { query: 'q', results_limit: 10 }, null).facts).toEqual(['“q” · 最多 10 条'])
    expect(approvalQuestion('web_search', 'web_search', {})).toBe('要我搜一下公开网络吗？')
  })

  it('shows a memory note\'s file and scope beside its own scrollable body', () => {
    const project = approvalFacts('memory_note', { filename: 'note.md', note: '# body', scope: 'project' }, null)
    expect(project.facts).toEqual(['note.md · 项目记忆'])
    expect(project.bodyMarkdown).toBe('# body')
    const global = approvalFacts('memory_note', { filename: 'n.md', note: 'x', scope: 'global' }, null)
    expect(global.facts).toEqual(['n.md · 全局记忆'])
    // The scope defaults to the project's own memory.
    expect(approvalFacts('memory_note', { filename: 'n.md' }, null).facts).toEqual(['n.md · 项目记忆'])
    expect(approvalQuestion('memory_note', 'memories_add_ad_hoc_note', {})).toBe('要我把这条记进长期记忆吗？')
  })

  it('shows the protected read\'s path and asks as Forebrain\'s own file', () => {
    expect(approvalFacts('protected_read', { file_path: 'forebrain.yaml', resolved_file_path: '~/.forebrain/forebrain.yaml' }, null).facts)
      .toEqual(['~/.forebrain/forebrain.yaml'])
    expect(approvalQuestion('protected_read', 'read_file', {})).toBe('这是 Forebrain 自己的配置文件，要读吗？')
  })

  it('shows no fact for the fallback — the only thing it knows is the question', () => {
    expect(approvalFacts('fallback', { justification: 'some reason' }, null).facts).toEqual([])
    expect(approvalQuestion('fallback', 'lsp', {})).toBe('要允许这次调用吗？')
  })

  it('parses the engine\'s payload and survives one it cannot parse', () => {
    expect(parseApprovalPayload('{"command":"ls"}')).toEqual({ command: 'ls' })
    expect(parseApprovalPayload('not json')).toEqual({})
    expect(parseApprovalPayload('[1,2]')).toEqual({})
  })
})
