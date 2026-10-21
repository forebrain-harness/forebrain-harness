import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { forebrainApi, type ActionRecord, type SessionApprovalRequest } from '@/lib/api'
import { setLocale } from '@/locales'
import ApprovalCard from './ApprovalCard.vue'

/**
 * An approval card replays the one line the surface printed for the decision:
 * a card with a line shows the line and never the stored reason; a card
 * without one prints nothing.
 *
 * The requests it hosts in the conversation's flow are the run's own questions.
 * A fetch gate is the question the terminal asks - which page, and what to do
 * about it - so it says the page and the three ways out, never the tool's name
 * or the action's id.
 */
describe('ApprovalCard', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))
  afterEach(() => vi.restoreAllMocks())

  function card(block: {
    actionId: string
    actionKind: string
    status: string
    confirmation?: string
    message?: string
  }) {
    const wrapper = mount(ApprovalCard, { props: { block } })
    return { wrapper, text: wrapper.text() }
  }

  it('replays the line the surface printed for the decision', () => {
    const got = card({
      actionId: 'act-1',
      actionKind: 'exit_plan_mode',
      status: 'denied',
      confirmation: "✗ You canceled forebrain's request to exit plan mode",
      message: 'canceled by user',
    })
    expect(got.text).toBe("✗ You canceled forebrain's request to exit plan mode")
    expect(got.text).not.toContain('canceled by user')
    expect(got.wrapper.find('[data-approval-status="denied"]').exists()).toBe(true)
  })

  it('prints nothing for a decision no surface printed a line for', () => {
    const got = card({
      actionId: 'act-2',
      actionKind: 'exit_plan_mode',
      status: 'denied',
      message: 'plan-review:delivered',
    })
    expect(got.wrapper.find('[data-approval-status]').exists()).toBe(false)
  })

  it('asks about the page a fetch needs, and offers the three answers', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      {
        id: 'act-fetch',
        kind: 'web_fetch',
        status: 'pending',
        payloadJson: JSON.stringify({ url: 'https://example.com/pricing' }),
        createdAt: 1,
        updatedAt: 1,
        permissionSuggestion: {
          permissionToolName: 'WebFetch',
          permissionInput: 'https://example.com/pricing',
          prefixRuleContent: 'domain:example.com',
          availableDecisions: ['accept', 'accept_and_remember', 'cancel'],
        },
      } as ActionRecord,
    ])
    const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    const gate = wrapper.find('[data-testid="web-fetch-approval"]')
    expect(gate.exists()).toBe(true)
    expect(gate.text()).toContain('Fetch this page?')
    expect(wrapper.find('[data-testid="web-fetch-page"]').text()).toBe('https://example.com/pricing')
    // Every answer says what it does, and the one that remembers names its scope.
    expect(gate.findAll('button').map((b) => b.text())).toEqual([
      'Fetch the page',
      "Don't ask again for example.com",
      "Don't fetch",
    ])
    // Nothing on the card is the tool's own name or an action id, and a card
    // whose button states the scope does not explain it again in a box.
    expect(gate.text()).not.toContain('web_fetch')
    expect(gate.text()).not.toContain('act-fetch')
    expect(gate.text()).not.toContain('Permission memory')
    wrapper.unmount()
  })

  // The two routing facts the engine writes into the payload are the only
  // place the card can learn them: a plain fetch says nothing extra, and a
  // routed one says who fetches and whether private space was opened.
  it('draws the fetch gate\'s routing facts only when the payload carries them', async () => {
    const mountFetch = async (payload: Record<string, unknown>) => {
      vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
        {
          id: 'act-fetch',
          kind: 'web_fetch',
          status: 'pending',
          payloadJson: JSON.stringify(payload),
          createdAt: 1,
          updatedAt: 1,
          permissionSuggestion: {
            permissionToolName: 'WebFetch',
            permissionInput: 'https://example.com/pricing',
            prefixRuleContent: 'domain:example.com',
            availableDecisions: ['accept', 'accept_and_remember', 'cancel'],
          },
        } as ActionRecord,
      ])
      const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
      await flushPromises()
      return wrapper
    }

    const routed = await mountFetch({
      url: 'https://example.com/pricing',
      rule_content: 'domain:example.com',
      routed_provider: 'deepseek',
      allow_private_ip: true,
    })
    expect(routed.findAll('[data-testid="web-fetch-fact"]').map((row) => row.text())).toEqual([
      'Fetched server-side by deepseek',
      'Private addresses are allowed',
    ])
    routed.unmount()

    // A plain fetch is the card it always was: no fact row at all.
    const plain = await mountFetch({ url: 'https://example.com/pricing', rule_content: 'domain:example.com' })
    expect(plain.findAll('[data-testid="web-fetch-fact"]')).toHaveLength(0)
    plain.unmount()
  })

  // One row per family the engine can park a run on, in the shapes its
  // payloads actually take. What is asserted is the whole card as the reader
  // meets it: the question, the fact lines in order, the decision buttons in
  // the engine's order — and never the tool's internal name or an action id.
  const familyRows: {
    id: string
    kind: string
    payload: Record<string, unknown>
    decisions: unknown[]
    question: string
    facts: string[]
    buttons: string[]
    suggestion?: Record<string, unknown>
  }[] = [
    {
      id: 'act-write-new', kind: 'write_file', payload: { file_path: 'new.go', content: 'x\n'.repeat(96) },
      decisions: ['accept', 'accept_for_session', 'cancel'],
      question: '要新建这个文件吗？', facts: ['96 行'], buttons: ['写入', '本会话不再问', '不要写'],
    },
    {
      id: 'act-write-over', kind: 'write_file', payload: { file_path: 'api.ts', content: 'x\n'.repeat(12), existing_lines: 3 },
      decisions: ['accept', 'accept_for_session', 'cancel'],
      question: '要覆盖这个文件吗？', facts: ['+12 −3 行'], buttons: ['覆盖', '本会话不再问', '不要写'],
    },
    {
      id: 'act-edit', kind: 'edit_file', payload: { file_path: 'a.go', old_string: 'one\ntwo', new_string: 'one\nnew' },
      decisions: ['accept', 'accept_for_session', 'cancel'],
      question: '要改这个文件吗？', facts: ['+2 −2 行'], buttons: ['修改', '本会话不再问', '不要改'],
    },
    {
      id: 'act-patch', kind: 'apply_patch',
      payload: { apply_patch: true, resolved_paths: ['/w/a.go', '/w/b.go'], patch: '*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n' },
      decisions: ['accept', 'accept_for_session', 'cancel'],
      question: '要应用这个补丁吗？', facts: ['2 个文件 · +1 −1'], buttons: ['应用', '本会话不再问', '不要应用'],
    },
    {
      id: 'act-shell-danger', kind: 'shell',
      payload: { command: 'rm -rf build/ && go build ./...', approval_reason: 'dangerous_shell_syntax', force_tool_approval: true, dangerous_rule: 'forced_rm' },
      decisions: ['accept', 'accept_and_remember', 'cancel'],
      question: '要运行这条命令吗？', facts: ['强制删除文件（rm -f），引擎把这类命令列为危险命令'], buttons: ['运行', '以后都不再问', '不要运行'],
    },
    {
      id: 'act-shell-unknown', kind: 'shell',
      payload: { command: 'mvn clean verify', approval_reason: 'unknown_shell_command' },
      decisions: ['accept', 'cancel'],
      question: '要运行这条命令吗？', facts: [], buttons: ['运行', '不要运行'],
    },
    {
      id: 'act-sandbox', kind: 'shell',
      payload: {
        command: 'cp bin/forebrain ~/.local/bin/', sandbox_permissions: 'require_escalated', approval_reason: 'sandbox_denied',
        sandbox_denial_reason: 'write outside project roots', force_tool_approval: true,
        justification: 'The sandbox blocked this command: write outside project roots. Allow it to run outside the sandbox?',
      },
      decisions: ['accept', 'accept_for_session', 'cancel'],
      question: '沙箱拦下了这条命令，要在沙箱外运行吗？', facts: ['沙箱拦下了这条命令', 'write outside project roots'], buttons: ['在沙箱外运行', '这类命令不再问', '不要运行'],
    },
    {
      id: 'act-sandbox-scoped', kind: 'shell',
      payload: {
        command: 'cp bin/forebrain /tmp/out', approval_reason: 'sandbox_denied', scoped_sandbox_retry: true,
        sandbox_denial_reason: 'write outside project roots',
        additional_permissions: { file_system: { entries: [{ path: { type: 'path', path: '/tmp/out' }, access: 'write' }] } },
        justification: 'The sandbox blocked writing to /tmp/out. Allow this command to write only there?',
      },
      decisions: ['accept', 'accept_for_session', 'cancel'],
      question: '沙箱拦下了这条命令，要在沙箱外运行吗？', facts: ['可写 /tmp/out'], buttons: ['在沙箱外运行', '这类命令不再问', '不要运行'],
    },
    {
      id: 'act-network', kind: 'shell',
      payload: { command: 'gh issue list', approval_reason: 'network_denied' },
      decisions: ['accept', 'accept_for_session', { apply_network_policy_amendment: { network_policy_amendment: { host: 'api.github.com', action: 'allow' } } }, 'cancel'],
      question: '要让它访问外网吗？', facts: ['api.github.com · HTTPS · 443'],
      buttons: ['允许', '本会话都允许', '以后都允许', '不要访问'],
      suggestion: { networkApprovalContext: { host: 'api.github.com', protocol: 'https' }, networkPort: 443 },
    },
    {
      id: 'act-permissions', kind: 'request_permissions',
      payload: {
        request_permissions: true,
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
      decisions: ['grant_for_turn', 'grant_for_turn_with_strict_auto_review', 'grant_for_session', 'decline'],
      question: '要给它这些权限吗？', facts: ['可读 ~/notes', '可写 /tmp/out', '可访问网络'],
      buttons: ['本轮允许', '本轮允许，自动审查', '本会话允许', '不授予'],
    },
    {
      id: 'act-enter-plan', kind: 'enter_plan_mode', payload: { reason: '改动跨三个包，先写计划再动手', action: 'enter' },
      decisions: ['accept', 'cancel'],
      question: '要进入计划模式吗？', facts: ['改动跨三个包，先写计划再动手'], buttons: ['进入', '不要进入'],
    },
    {
      id: 'act-mcp', kind: 'mcp__github__create_issue',
      payload: { repo: 'unionj-cloud/forebrain-harness', title: 'cookie login still 401' },
      decisions: ['accept', 'accept_for_session', 'accept_and_remember', 'cancel'],
      question: '要调用 github 的 create_issue 吗？', facts: ['Repo: unionj-cloud/forebrain-harness', 'Title: cookie login still 401'],
      buttons: ['调用', '本会话不再问', '以后都不再问', '不要调用'],
    },
    {
      id: 'act-protected-read', kind: 'read_file',
      payload: {
        file_path: 'forebrain.yaml', resolved_file_path: '~/.forebrain/forebrain.yaml',
        force_tool_approval: true, approval_reason: 'protected_path',
        justification: 'Reading ~/.forebrain/forebrain.yaml is inside Forebrain\'s own home.',
      },
      decisions: ['accept', 'accept_for_session', 'cancel'],
      question: '这是 Forebrain 自己的配置文件，要读吗？',
      facts: ['Reading ~/.forebrain/forebrain.yaml is inside Forebrain\'s own home.', '~/.forebrain/forebrain.yaml'],
      buttons: ['读取', '本会话不再问', '不要读'],
    },
    {
      id: 'act-search', kind: 'web_search', payload: { query: 'forebrain harness', results_limit: 5 },
      decisions: ['accept', 'cancel'],
      question: '要我搜一下公开网络吗？', facts: ['“forebrain harness” · 最多 5 条'], buttons: ['搜索', '不要搜'],
    },
    {
      id: 'act-memory', kind: 'memories_add_ad_hoc_note', payload: { filename: 'note.md', note: '# 一条要记的话', scope: 'project' },
      decisions: ['accept', 'decline'],
      question: '要我把这条记进长期记忆吗？', facts: ['note.md · 项目记忆'], buttons: ['记住这条', '不要记'],
    },
    {
      // A call no family claimed still leads with the engine's reason: it is
      // the only grounded thing that card has to say.
      id: 'act-fallback', kind: 'lsp', payload: { operation: 'workspace_symbols', query: 'Gateway', justification: 'The runtime asked for this read.' },
      decisions: ['accept', 'cancel'],
      question: '要允许这次调用吗？', facts: ['The runtime asked for this read.'], buttons: ['允许', '不要'],
    },
  ]

  it('draws every family as its own question, facts, and decision buttons', async () => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue(familyRows.map((row) => ({
      id: row.id,
      kind: row.kind,
      status: 'pending',
      payloadJson: JSON.stringify(row.payload),
      createdAt: 1,
      updatedAt: 1,
      permissionSuggestion: {
        permissionToolName: row.kind,
        ...(row.suggestion ?? {}),
        availableDecisions: row.decisions,
      },
    })) as ActionRecord[])
    const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    for (const row of familyRows) {
      const card = wrapper.find(`[data-action-id="${row.id}"]`)
      expect(card.exists(), row.id).toBe(true)
      expect(card.text(), row.id).toContain(row.question)
      const factRows = card.findAll('[data-testid="approval-gate-fact"]')
      expect(factRows.map((r) => r.text()), row.id).toEqual(row.facts)
      const labels = card.findAll('[data-testid^="pending-decision-"]').map((b) => b.text())
      expect(labels, row.id).toEqual(row.buttons)
      // The card never says the tool's internal name or an action id.
      expect(card.text(), row.id).not.toContain(row.kind)
      expect(card.text(), row.id).not.toContain(row.id)
    }

    // The memory note carries its own body, rendered as the Markdown it is and
    // scrollable rather than cut. The renderer types its content out, so the
    // heading arrives a tick after the mount.
    const body = wrapper.find('[data-action-id="act-memory"] [data-testid="approval-gate-body"]')
    expect(body.exists()).toBe(true)
    await vi.waitFor(() => expect(body.text()).toContain('一条要记的话'), { timeout: 2000 })
    wrapper.unmount()
    setLocale('en')
  })

  // The ask card's footer prints Esc and「和它聊聊」; both are real here. Esc sends
  // the engine the cancel decision — the same verdict the terminal's overlay
  // returns — and「和它聊聊」drops the question and hands the caret back to the
  // composer.
  it('cancels an asked question with the decision the engine understands', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      {
        id: 'act-ask',
        kind: 'user_interaction',
        status: 'pending',
        payloadJson: JSON.stringify({
          questions: [
            { id: 'scope', prompt: '要动哪些范围？', options: [{ id: 'tui', label: 'TUI' }, { id: 'web', label: 'Web' }], allow_other: true },
            { id: 'how', prompt: '这次怎么交付？', options: [{ id: 'pr', label: 'PR' }], allow_other: true },
          ],
        }),
        createdAt: 1,
        updatedAt: 1,
      } as ActionRecord,
    ])
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    const card = wrapper.find('[data-testid="ask-card"]')
    expect(card.exists()).toBe(true)
    // No progress count in the head: the design has none, and it used to print
    // one past its own total on the review screen.
    expect(wrapper.find('[data-testid="pending-question"]').text()).not.toMatch(/\d+\/\d+/)

    await card.trigger('keydown', { key: 'Escape' })
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-ask', { decision: 'cancel' })
    wrapper.unmount()
  })

  it('hands the composer the turn when「和它聊聊」is picked', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      {
        id: 'act-ask',
        kind: 'user_interaction',
        status: 'pending',
        payloadJson: JSON.stringify({
          questions: [{ id: 'scope', prompt: '要动哪些范围？', options: [{ id: 'tui', label: 'TUI' }], allow_other: true }],
        }),
        createdAt: 1,
        updatedAt: 1,
      } as ActionRecord,
    ])
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    await wrapper.find('[data-testid="ask-chat-about"]').trigger('click')
    await flushPromises()
    // One gesture, both halves: the question is dropped and the caller is told
    // to put the caret in the composer.
    expect(approve).toHaveBeenCalledWith('act-ask', { decision: 'cancel' })
    expect(wrapper.emitted('focus-composer')).toHaveLength(1)
    wrapper.unmount()
  })

  it('keeps the exit-plan approval its own card', async () => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      { id: 'act-exit', kind: 'exit_plan_mode', status: 'pending', payloadJson: JSON.stringify({ session_id: 's1' }), createdAt: 1, updatedAt: 1 } as ActionRecord,
    ])
    vi.spyOn(forebrainApi, 'sessionApprovalRequest').mockResolvedValue({
      actionId: 'act-exit', kind: 'exit_plan_mode', planText: '# 计划', planReviewModels: [], planReviews: [], planReviewActive: null,
    } as SessionApprovalRequest)
    const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exit-plan-approval"]')
    expect(card.exists()).toBe(true)
    expect(card.text()).toContain('退出计划模式')
    // The feedback field is not on the card before its verdict is chosen.
    expect(card.find('[data-testid="exit-plan-feedback"]').exists()).toBe(false)
    wrapper.unmount()
    setLocale('en')
  })

  it('opens the plan feedback field only after「继续规划」, and folds it on「批准」', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      { id: 'act-exit', kind: 'exit_plan_mode', status: 'pending', payloadJson: JSON.stringify({ session_id: 's1' }), createdAt: 1, updatedAt: 1 } as ActionRecord,
    ])
    vi.spyOn(forebrainApi, 'sessionApprovalRequest').mockResolvedValue({
      actionId: 'act-exit', kind: 'exit_plan_mode', planText: '# 计划', planReviewModels: [], planReviews: [], planReviewActive: null,
    } as SessionApprovalRequest)
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const wrapper = mount(ApprovalCard, { props: { sessionId: 's1', version: 0 } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exit-plan-approval"]')
    expect(card.find('input').exists()).toBe(false)

    await wrapper.find('[data-testid="exit-plan-keep-planning"]').trigger('click')
    await flushPromises()
    expect(card.find('input').exists()).toBe(true)

    await wrapper.find('[data-testid="exit-plan-approve"]').trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-exit', {})
    expect(card.find('input').exists()).toBe(false)
    wrapper.unmount()
  })
})
