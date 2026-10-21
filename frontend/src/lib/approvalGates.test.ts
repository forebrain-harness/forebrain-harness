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
    // A read that asks is the protected-path read, and the engine says which
    // reads those are by tagging the payload — not by which tool ran. `lsp`
    // reads through the same authorizeRead (pkg/tool/search.go) and asks the
    // same question; an ordinary read never parks the run at all.
    const protectedRead = {
      force_tool_approval: true, approval_reason: 'protected_path',
      file_path: 'forebrain.yaml', resolved_file_path: '/h/.forebrain/forebrain.yaml',
    }
    expect(approvalFamily('read_file', protectedRead, null)).toBe('protected_read')
    expect(approvalFamily('lsp', protectedRead, null)).toBe('protected_read')
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

  it('draws the edit the engine diffed, and the old texts only when it could not', () => {
    // The payload the engine writes (pkg/tool/file_tools.go via pkg/event.Build):
    // its own line counts and the first hunk of its unified diff. The card draws
    // those rows, so both surfaces describe one edit the same way.
    const engineDiffed = approvalFacts('file_edit', {
      file_path: 'a.go', resolved_file_path: '/w/a.go',
      old_string: 'a\nb\nc', new_string: 'a\nB\nc',
      added_lines: 1, removed_lines: 1,
      unified_diff: '@@ -1,3 +1,3 @@\n a\n-b\n+B\n c',
    }, null)
    expect(engineDiffed.facts).toEqual(['+1 −1 行'])
    expect(engineDiffed.added).toBe(1)
    expect(engineDiffed.removed).toBe(1)
    // The @@ header is a context row, not a body row: its sign slot is empty.
    expect(engineDiffed.diff).toEqual([['', '@@ -1,3 +1,3 @@'], ['', ' a'], ['-', 'b'], ['+', 'B'], ['', ' c']])
    expect(approvalQuestion('file_edit', 'edit_file', {})).toBe('要改这个文件吗？')

    // A historical action has neither field: the two texts are all there is, and
    // the card falls back to them without throwing.
    const historical = approvalFacts('file_edit', { file_path: 'a.go', old_string: 'one\ntwo', new_string: 'one\nnew\nextra' }, null)
    expect(historical.facts).toEqual(['+3 −2 行'])
    expect(historical.diff).toEqual([['-', 'one'], ['-', 'two'], ['+', 'one'], ['+', 'new'], ['+', 'extra']])

    // A written 0 is the engine saying nothing changed — a fact, not a missing
    // value, so the card reads 0/0 rather than falling back to the texts and
    // printing NaN.
    const noop = approvalFacts('file_edit', { old_string: 'x', new_string: 'x', added_lines: 0, removed_lines: 0, unified_diff: '' }, null)
    expect(noop.facts).toEqual(['+0 −0 行'])
    expect(noop.added).toBe(0)
    expect(noop.removed).toBe(0)
    expect(noop.facts.join(' ')).not.toContain('NaN')
  })

  it('counts a patch from its resolved paths and body, and shows its first hunk verbatim', () => {
    const payload = {
      apply_patch: true,
      resolved_paths: ['/w/pkg/a.go', '/w/pkg/b.go', '/w/frontend/c.ts'],
      patch: '*** Begin Patch\n*** Update File: pkg/a.go\n@@\n ctx\n-old\n+new\n ctx\n*** Update File: pkg/b.go\n@@\n-old2\n+new2\n',
    }
    const facts = approvalFacts('apply_patch', payload, null)
    // One line of facts, the way the prototype prints them (:3503).
    expect(facts.facts).toEqual(['3 个文件 · +2 −2'])
    expect(facts.diff).toEqual([['', '@@'], ['', ' ctx'], ['-', 'old'], ['+', 'new'], ['', ' ctx']])
    expect(approvalQuestion('apply_patch', 'apply_patch', payload)).toBe('要应用这个补丁吗？')
    // Eight body rows is all a patch card shows; the bound belongs to the
    // caller and did not move when the reader learned a second bound.
    const wide = approvalFacts('apply_patch', {
      apply_patch: true,
      patch: '*** Begin Patch\n*** Update File: pkg/a.go\n@@\n' + Array.from({ length: 20 }, (_, i) => `+row-${i}`).join('\n'),
    }, null)
    expect(wide.diff).toHaveLength(9)
    expect(wide.diff![0]).toEqual(['', '@@'])
    expect(wide.diff![8]).toEqual(['+', 'row-7'])
  })

  it('says what the one dangerous rule does, and nothing when the engine named no rule', () => {
    // The payload carries the rule the engine matched; the sentence is that
    // conclusion, with no path and no consequence invented around it.
    expect(approvalFacts('shell', {
      command: 'rm -rf build/ && go build ./...', approval_reason: 'dangerous_shell_syntax', dangerous_rule: 'forced_rm',
    }, null).facts).toEqual(['强制删除文件（rm -f），引擎把这类命令列为危险命令'])
    // A command the engine's own parser could not read (it gives up on `*`) is
    // not "no rule": it is whatever the engine answered. Nothing is re-derived
    // here, so the glob never reaches the card.
    const glob = approvalFacts('shell', { command: 'rm -f *.log', dangerous_rule: 'forced_rm' }, null)
    expect(glob.facts).toEqual(['强制删除文件（rm -f），引擎把这类命令列为危险命令'])
    expect(glob.facts.join(' ')).not.toContain('*.log')
    // A rule this build has no sentence for shows no line: the card does not
    // paraphrase a rule it does not know.
    expect(approvalFacts('shell', { command: 'x', dangerous_rule: 'something_new' }, null).facts).toEqual([])
    // Complex parsing with no dangerous rule names none, so the card has none.
    expect(approvalFacts('shell', { command: 'mvn clean verify', approval_reason: 'unknown_shell_command' }, null).facts).toEqual([])
    expect(approvalQuestion('shell', 'shell', {})).toBe('要运行这条命令吗？')
  })

  it('says the sandbox boundary plus the reason the sandbox gave, verbatim', () => {
    // The denial may be about the network as readily as about the filesystem,
    // so the card quotes the engine instead of asserting which one it was.
    expect(approvalFacts('shell_outside_sandbox', {
      approval_reason: 'sandbox_denied',
      sandbox_denial_reason: 'listen tcp 127.0.0.1:0: bind: operation not permitted',
      justification: 'The sandbox blocked this command: listen tcp 127.0.0.1:0: bind: operation not permitted. Allow it to run outside the sandbox?',
    }, null).facts).toEqual([
      '沙箱拦下了这条命令',
      'listen tcp 127.0.0.1:0: bind: operation not permitted',
    ])
    expect(approvalFacts('shell_outside_sandbox', {}, null).facts).toEqual(['沙箱拦下了这条命令'])
    // The scoped retry the engine writes: the scope it would gain, and — since
    // the engine also names the denial it is retrying — no second sentence.
    const scoped = {
      approval_reason: 'sandbox_denied',
      scoped_sandbox_retry: true,
      sandbox_denial_reason: 'write outside project roots',
      additional_permissions: {
        file_system: { entries: [{ path: { type: 'path', path: '/tmp/out' }, access: 'write' }] },
      },
      justification: 'The sandbox blocked writing to /tmp/out. Allow this command to write only there?',
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

  it('reads a permission entry\'s location out of the object the engine marshals', () => {
    // The wire shape (pkg/safety/permission_types.go FileSystemPermissionEntry):
    // path is an object, and a deny entry or a missing_path_behavior is what
    // makes the whole profile marshal as entries[]. Reading the entry's path
    // object as a string is what used to print a stringified object instead.
    const payload = {
      request_permissions: true,
      permissions: {
        file_system: {
          entries: [
            { path: { type: 'path', path: '~/notes' }, access: 'read' },
            { path: { type: 'path', path: '/tmp/out' }, access: 'write' },
            { path: { type: 'path', path: '/etc/shadow' }, access: 'deny' },
          ],
          glob_scan_max_depth: 4,
        },
        network: { enabled: true },
      },
    }
    const facts = approvalFacts('request_permissions', payload, null)
    // A deny entry grants nothing, so it has no line; the network grant has no
    // host list on the wire, so it is stated as the one thing it is.
    expect(facts.facts).toEqual(['可读 ~/notes', '可写 /tmp/out', '可访问网络'])
    expect(facts.facts.join(' ')).not.toContain('[object Object]')
    expect(facts.permissions).toEqual([{ access: 'read', path: '~/notes' }, { access: 'write', path: '/tmp/out' }])
    expect(facts.network).toBe(true)
    expect(approvalQuestion('request_permissions', 'request_permissions', payload)).toBe('要给它这些权限吗？')

    // A glob pattern and a named location each read as themselves.
    const named = approvalFacts('request_permissions', {
      permissions: {
        file_system: {
          entries: [
            { path: { type: 'glob_pattern', pattern: 'src/**/*.ts' }, access: 'read' },
            { path: { type: 'special', value: { kind: 'project_roots', subpath: 'frontend' } }, access: 'write' },
            { path: { type: 'special', value: { kind: 'tmpdir' } }, access: 'read' },
          ],
        },
      },
    }, null)
    expect(named.facts).toEqual(['可读 src/**/*.ts', '可写 project_roots/frontend', '可读 tmpdir'])
    expect(named.network).toBeUndefined()

    // The legacy read/write arrays the same profile still marshals for plain
    // paths keep working, and an entry whose location cannot be read drops out
    // instead of printing an empty line.
    expect(approvalFacts('request_permissions', {
      permissions: { file_system: { read: ['/a'], write: ['/b'] } },
    }, null).facts).toEqual(['可读 /a', '可写 /b'])
    expect(approvalFacts('request_permissions', {
      permissions: { file_system: { entries: [{ path: { type: 'special', value: {} }, access: 'read' }] } },
    }, null).facts).toEqual([])
  })

  it('shows the model\'s own reason for plan mode, and nothing when it wrote none', () => {
    expect(approvalFacts('enter_plan_mode', { reason: '改动跨三个包，先写计划再动手' }, null).facts)
      .toEqual(['改动跨三个包，先写计划再动手'])
    expect(approvalFacts('enter_plan_mode', { reason: '   ' }, null).facts).toEqual([])
    expect(approvalQuestion('enter_plan_mode', 'enter_plan_mode', {})).toBe('要进入计划模式吗？')
  })

  it('labels an MCP call\'s first scalar inputs, one line each and never a wall', () => {
    const payload = {
      repo: 'unionj-cloud/forebrain-harness',
      title: 'cookie login still 401',
      body: 'x'.repeat(500),
      labels: ['bug'],
      nested: { x: 1 },
    }
    const facts = approvalFacts('mcp_tool', payload, null)
    // Key order, so the same call always reads the same; three lines at most,
    // under the labels the keys read as rather than the keys themselves.
    expect(facts.facts.map((line) => line.split(':')[0])).toEqual(['Body', 'Repo', 'Title'])
    expect(facts.facts[0]).toBe(`Body: ${'x'.repeat(160)}…`)
    expect(facts.facts[1]).toBe('Repo: unionj-cloud/forebrain-harness')
    expect(facts.facts[2]).toBe('Title: cookie login still 401')
    // A multi-line value is its first line, collapsed and trimmed.
    const multi = approvalFacts('mcp_tool', {
      patch: '*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n',
      notes: '  two   spaces\nand a second line',
    }, null)
    expect(multi.facts).toEqual(['Notes: two spaces', 'Patch: *** Begin Patch'])
    expect(multi.facts.some((line) => line.includes('\n'))).toBe(false)
    expect(approvalQuestion('mcp_tool', 'mcp__github__create_issue', payload)).toBe('要调用 github 的 create_issue 吗？')
  })

  it('quotes the search and its cap, falling back to the tool default', () => {
    expect(approvalFacts('web_search', { query: '  forebrain harness  ' }, null).facts).toEqual(['“forebrain harness” · 最多 5 条'])
    expect(approvalFacts('web_search', { query: 'q', results_limit: 10 }, null).facts).toEqual(['“q” · 最多 10 条'])
    // The tool's own ceiling: the card never promises more than one call can ask.
    expect(approvalFacts('web_search', { query: 'q', results_limit: 25 }, null).facts).toEqual(['“q” · 最多 10 条'])
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

  it('shows the protected read\'s path and the engine\'s reason, for any reading tool', () => {
    expect(approvalFacts('protected_read', { file_path: 'forebrain.yaml', resolved_file_path: '~/.forebrain/forebrain.yaml' }, null).facts)
      .toEqual(['~/.forebrain/forebrain.yaml'])
    expect(approvalQuestion('protected_read', 'read_file', {})).toBe('这是 Forebrain 自己的配置文件，要读吗？')
    // An `lsp` read falls through the same authorizeRead, so it asks the same
    // question with the same facts — the reason first, then the file.
    const lsp = approvalFacts('protected_read', {
      operation: 'workspace_symbols',
      file_path: '~/.forebrain/forebrain.yaml',
      resolved_file_path: '/home/u/.forebrain/forebrain.yaml',
      force_tool_approval: true,
      approval_reason: 'protected_path',
      justification: 'Reading /home/u/.forebrain/forebrain.yaml is inside Forebrain\'s own home.',
    }, null)
    expect(lsp.facts).toEqual([
      'Reading /home/u/.forebrain/forebrain.yaml is inside Forebrain\'s own home.',
      '/home/u/.forebrain/forebrain.yaml',
    ])
    expect(lsp.rationale).toBe('Reading /home/u/.forebrain/forebrain.yaml is inside Forebrain\'s own home.')
  })

  it('leads every family with the engine\'s own sentence, once', () => {
    // A plan-mode command the engine could not prove read-only: the sentence
    // the user is being asked about is the whole point of the prompt.
    const planMode = approvalFacts('shell', {
      command: 'python3 script.py',
      force_tool_approval: true,
      approval_reason: 'plan_mode_unproven_command',
      justification: 'Plan mode is active: this command could not be proven read-only.',
    }, null)
    expect(planMode.facts).toEqual(['Plan mode is active: this command could not be proven read-only.'])
    // A family that already says it does not say it twice.
    const dangerous = approvalFacts('shell', {
      command: 'rm -f /tmp/x', dangerous_rule: 'forced_rm', justification: '强制删除文件（rm -f），引擎把这类命令列为危险命令',
    }, null)
    expect(dangerous.facts).toEqual(['强制删除文件（rm -f），引擎把这类命令列为危险命令'])
    // The sandbox branch renders the payload's own reason, so its rationale
    // slot stays empty rather than restating it in a second sentence.
    expect(approvalFacts('shell_outside_sandbox', {
      sandbox_denial_reason: 'write outside roots',
      justification: 'The sandbox blocked this command: write outside roots. Allow it to run outside the sandbox?',
    }, null).rationale).toBeUndefined()
    // An escalation the engine never refused has no denial reason to render, so
    // the sentence the model wrote for it is all the card has to go on.
    expect(approvalFacts('shell_outside_sandbox', {
      sandbox_permissions: 'require_escalated',
      justification: 'Installing dependencies writes outside the project, so ask before doing it.',
    }, null).facts).toEqual(['Installing dependencies writes outside the project, so ask before doing it.', '沙箱拦下了这条命令'])
  })

  it('adds the fetch\'s routing facts only when the payload carries them', () => {
    // A plain fetch says nothing extra: the card's own lines cover it.
    expect(approvalFacts('web_fetch', { url: 'https://example.org/page' }, null).facts).toEqual([])
    expect(approvalFacts('web_fetch', {
      url: 'https://example.org/page',
      routed_provider: 'deepseek',
      allow_private_ip: true,
    }, null).facts).toEqual(['由 deepseek 在服务端取回', '私网地址已放开'])
    // Each fact stands on its own: an empty provider is not a provider.
    expect(approvalFacts('web_fetch', { routed_provider: '' }, null).facts).toEqual([])
    expect(approvalFacts('web_fetch', { allow_private_ip: true }, null).facts).toEqual(['私网地址已放开'])
    // The family stays the fetch gate's own, so the card keeps its shape.
    expect(approvalFamily('web_fetch', { routed_provider: 'deepseek' }, null)).toBe('web_fetch')
  })

  it('gives the fallback the engine\'s reason and nothing it invented', () => {
    // The reason is the only thing this card can honestly say: it has no rule of
    // its own to quote. With none in the payload it says nothing extra.
    expect(approvalFacts('fallback', { justification: 'you asked' }, null).facts).toEqual(['you asked'])
    expect(approvalFacts('fallback', {}, null).facts).toEqual([])
    expect(approvalQuestion('fallback', 'lsp', {})).toBe('要允许这次调用吗？')
  })

  it('parses the engine\'s payload and survives one it cannot parse', () => {
    expect(parseApprovalPayload('{"command":"ls"}')).toEqual({ command: 'ls' })
    expect(parseApprovalPayload('not json')).toEqual({})
    expect(parseApprovalPayload('[1,2]')).toEqual({})
  })
})
