import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import tip from '@/directives/tip'

import { setLocale } from '@/locales'
import ToolCallCard from './ToolCallCard.vue'
import { usePendingActions } from '@/composables/usePendingActions'
import { PendingActionsKey } from '@/composables/providePendingActions'
import type { ActionRow } from '@/composables/usePendingActions'
import type { SubagentToolStep } from '@/composables/useChatStream'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

function stepOf(overrides: Partial<SubagentToolStep> = {}): SubagentToolStep {
  return {
    stepId: 'call-1',
    toolName: 'read_file',
    summary: 'Read shot.png · image 1.4 MB',
    status: 'completed',
    ...overrides,
  }
}

/**
 * An image step draws its pixels as a thumbnail fetched from the workspace raw
 * endpoint — never base64 in the step payload — and every other step draws no
 * <img> at all.
 */
describe('ToolCallCard', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => setLocale('en'))

  it('renders a thumbnail pointing at the raw endpoint for an image step', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        // Content renders only while the card is open.
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          previewKind: 'image',
          previewPath: '/proj/shot.png',
          previewWidth: 1200,
          previewHeight: 600,
          previewBytes: 1450000,
        }),
      },
    })

    const img = wrapper.find('img')
    expect(img.exists()).toBe(true)
    expect(img.attributes('src')).toBe('/api/workspace/file/raw?path=%2Fproj%2Fshot.png&session_id=s1')
    expect(img.attributes('loading')).toBe('lazy')
    expect(img.attributes('alt')).toContain('shot.png')
    expect(img.attributes('alt')).toContain('1200×600')
    // A click opens the full image in a new tab.
    expect(wrapper.find('a').attributes('href')).toBe(img.attributes('src'))
    expect(wrapper.find('a').attributes('target')).toBe('_blank')
  })

  it('renders no thumbnail for a text step', () => {
    const wrapper = mount(ToolCallCard, {
      props: { open: true, scope: { sessionId: 's1' }, step: stepOf() },
    })
    expect(wrapper.find('img').exists()).toBe(false)
  })

  it('renders no thumbnail when the caller supplied no scope', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        step: stepOf({ previewKind: 'image', previewPath: '/proj/shot.png', previewWidth: 1, previewHeight: 1 }),
      },
    })
    expect(wrapper.find('img').exists()).toBe(false)
  })

  it('captions the thumbnail with dimensions, bytes and scaled, omitting unknowns', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          previewKind: 'image',
          previewPath: '/proj/shot.png',
          previewWidth: 1200,
          previewHeight: 600,
          previewBytes: 1_450_000,
          previewScaled: true,
        }),
      },
    })
    // Aligned with the TUI's ▤ line: WxH · bytes · scaled.
    expect(wrapper.text()).toContain('1200×600')
    expect(wrapper.text()).toContain('1.4 MB')
    expect(wrapper.text()).toContain('scaled')

    const partial = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        // A refused or degraded block: dimensions parsed, no bytes, no scale.
        // The summary says no size either, so a stray byte count can only come
        // from the caption.
        step: stepOf({
          summary: 'Read shot.png (saved, not sent)',
          previewKind: 'image',
          previewPath: '/proj/shot.png',
          previewWidth: 1200,
          previewHeight: 600,
        }),
      },
    })
    expect(partial.text()).toContain('1200×600')
    expect(partial.text()).not.toContain('scaled')
    expect(partial.text()).not.toMatch(/\d+(\.\d+)? (KB|MB)/)
  })

  it('never announces 0×0 for an image whose dimensions are unknown', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({ previewKind: 'image', previewPath: '/proj/shot.webp' }),
      },
    })
    const alt = wrapper.find('img').attributes('alt')
    expect(alt).toContain('shot.webp')
    expect(alt).not.toContain('0×0')
  })

  it('draws an answered question as its question and its answers, opened', () => {
    // The runtime's settled body: one line per question, each answer on its
    // own line behind an arrow (pkg/tool/format.go FormatUserInteractionResult).
    const wrapper = mount(ToolCallCard, {
      props: {
        step: stepOf({
          toolName: 'user_interaction',
          summary: 'asked user',
          status: 'completed',
          output: '这次改动怎么交付？\n  → 设计方案并开发实施\n要动哪些范围？\n  → TUI\n  → 网关',
        }),
      },
    })
    const done = wrapper.get('[data-testid="ask-done"]')
    expect(done.findAll('.q').map((q) => q.text())).toEqual(['这次改动怎么交付？', '要动哪些范围？'])
    expect(done.findAll('.ans').map((a) => a.text())).toEqual([
      '→ 设计方案并开发实施',
      '→ TUI',
      '→ 网关',
    ])
    // A settled ask is drawn open: the answer is the point of the card.
    expect(wrapper.get('[data-slot="collapsible-trigger"]').attributes('aria-expanded')).toBe('true')
  })

  it('keeps a question that is still waiting as an ordinary call', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        step: stepOf({ toolName: 'user_interaction', summary: 'asked user', status: 'awaiting approval', output: '这次改动怎么交付？\n  → 设计方案并开发实施' }),
      },
    })
    expect(wrapper.find('[data-testid="ask-done"]').exists()).toBe(false)
  })

  it('keeps a short summary out of the body: no command block in the card', () => {
    const wrapper = mount(ToolCallCard, {
      props: { open: true, scope: { sessionId: 's1' }, step: stepOf() },
    })
    expect(wrapper.find('.cmd-full').exists()).toBe(false)
    expect(wrapper.find('.tool-card-body').text()).not.toContain(stepOf().summary)
  })

  it('holds a long command to one head line: the body is the result only', () => {
    const summary = `ran echo ${'x'.repeat(200)}`
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({ toolName: 'shell', summary }),
      },
    })
    // The command is the head; the body holds the result, not the command.
    expect(wrapper.find('.cmd-full').exists()).toBe(false)
    expect(wrapper.find('.tool-card-body').exists()).toBe(true)
    expect(wrapper.find('.tool-card-body').text()).not.toContain(summary)
    const head = wrapper.get('[data-slot="collapsible-trigger"]')
    const title = head.get('.ttl')
    // One line, whatever it holds — and no native tooltip: the eye button is
    // the way to see the whole of it.
    expect(title.attributes('title')).toBeUndefined()
  })

  it('offers the eye on a multi-line command and opens it as a command document', async () => {
    const command = 'cat <<\'EOF\' > /tmp/x\nbody\nEOF'
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          toolName: 'shell',
          summary: 'ran cat',
          header: `Ran ${command.replace(/\n/g, ' ')}`,
          headerParts: { action: 'Ran', target: command.replace(/\n/g, ' ') },
          input: { command },
        }),
      },
    })
    const eye = wrapper.find('[data-testid="tool-show-all"]')
    expect(eye.exists()).toBe(true)
    await eye.trigger('click')
    const emitted = wrapper.emitted('open-doc')
    expect(emitted).toHaveLength(1)
    expect(emitted![0]![0]).toMatchObject({ kind: 'command', command })
  })

  it('offers the eye only when the head line actually had to cut its text', async () => {
    const wrapper = mount(ToolCallCard, {
      props: { open: true, scope: { sessionId: 's1' }, step: stepOf({ toolName: 'shell', summary: 'ran ls' }) },
    })
    // jsdom lays nothing out, so nothing is cut: no eye for a line that fits.
    expect(wrapper.find('[data-testid="tool-show-all"]').exists()).toBe(false)

    // A head whose object overflows its room says so: the measured widths are
    // pinned the way a ResizeObserver would report them, and the card
    // re-measures when its head's words change.
    const long = `ran echo ${'x'.repeat(200)}`
    await wrapper.setProps({ step: stepOf({ toolName: 'shell', summary: long }) })
    const ttl = wrapper.element.querySelector('.ttl') as HTMLElement
    Object.defineProperty(ttl, 'scrollWidth', { configurable: true, value: 800 })
    Object.defineProperty(ttl, 'clientWidth', { configurable: true, value: 120 })
    await wrapper.setProps({ step: stepOf({ toolName: 'shell', summary: `${long} more` }) })
    expect(wrapper.find('[data-testid="tool-show-all"]').exists()).toBe(true)
  })

  // A call that never ran is canceled, not refused: in a conversation that
  // asks for no approvals nothing was denied, so the state word must never say so.
  // (Drawn off the line, where the state sits in the head as a badge.)
  it('says a call that never ran was canceled, and a refused one was denied', () => {
    const state = (status: string) => mount(ToolCallCard, {
      props: { onLine: false, step: stepOf({ status, summary: 'go test ./...', toolName: 'shell' }) },
    }).get('.tool-status')

    const canceled = state('canceled')
    expect(canceled.text()).toBe('Canceled')
    expect(canceled.classes()).toContain('tool-status--canceled')

    const denied = state('denied')
    expect(denied.text()).toBe('Denied')
    expect(denied.classes()).toContain('tool-status--denied')
  })

  // The head is the engine's header — the terminal's words — so only a shell
  // call says "Ran"; every other tool names what it did.
  it('heads the card with the engine header and draws the state as an icon', () => {
    const wrapper = mount(ToolCallCard, {
      props: { onLine: false, step: stepOf({ header: 'Read plans/100.md lines 1-96 · <0.1s', summary: 'ran read /abs/plans/100.md · lines 1-96' }) },
    })
    expect(wrapper.get('.ttl .solo').text()).toBe('Read plans/100.md lines 1-96 · <0.1s')
    expect(wrapper.text()).not.toContain('ran read')
    // The state is an icon with its word as the label, not a word in the header.
    const state = wrapper.get('.tool-status')
    expect(state.attributes('role')).toBe('img')
    expect(state.attributes('aria-label')).toBe('Done')
    expect(state.find('svg').exists()).toBe(true)
  })

  it('falls back to the summary for a step that arrived without a header', () => {
    const wrapper = mount(ToolCallCard, { props: { step: stepOf({ summary: 'ran read a.go' }) } })
    expect(wrapper.get('.ttl .solo').text()).toBe('ran read a.go')
  })

  it('draws a shell call’s body from its card facts — no Markdown body anywhere', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          toolName: 'shell',
          header: 'Ran go vet ./pkg/gateway/...',
          input: { command: 'go vet ./pkg/gateway/...' },
          shellCard: {
            stdout: 'no issues', stderr: 'pkg/gateway/ws.go:81: too many arguments',
            exitCode: 1,
            stdoutOmittedBytes: 0, stderrOmittedBytes: 0, omittedBytes: 0,
            retrieveId: 0, spoolOmittedBytes: 0,
            files: [], notes: [],
          },
        }),
      },
    })
    const body = wrapper.get('.tool-card-body')
    expect(body.text()).toContain('no issues')
    expect(body.text()).toContain('too many arguments')
    // The very things the old Markdown body put on screen are gone.
    expect(body.text()).not.toContain('**shell**')
    expect(body.text()).not.toContain('input:')
    expect(body.text()).not.toContain('output:')
    expect(body.text()).not.toContain('stdout:')
    expect(body.text()).not.toContain('stderr:')
    expect(body.text()).not.toContain('sandbox_backend')
    // A non-zero exit rides the head, left of the duration.
    expect(wrapper.get('[data-slot="collapsible-trigger"]').text()).toContain('Exit 1')
  })

  it('opens a file tool’s card with the unified diff, plain for a created file', async () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          toolName: 'edit_file',
          headerParts: { action: 'Edited', target: 'a.go' },
          fileDiff: {
            path: 'a.go', added: 1, deleted: 1,
            unifiedDiff: '@@ -1 +1 @@\n-old\n+new',
            rest: 'lsp diagnostics: 1 new in 1 file',
          },
        }),
      },
    })
    expect(wrapper.find('[data-testid="diff-block"]').exists()).toBe(true)
    expect(wrapper.findAll('.dr.add')).toHaveLength(1)
    expect(wrapper.findAll('.dr.del')).toHaveLength(1)
    // The rest of the body after the diff section renders as it always has
    // (the Markdown renderer types itself out, so the text arrives late).
    await vi.waitFor(() => expect(wrapper.text()).toContain('lsp diagnostics: 1 new in 1 file'))

    const created = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          toolName: 'write_file',
          headerParts: { action: 'Created', target: 'new.go' },
          fileDiff: { path: 'new.go', added: 2, deleted: 0, unifiedDiff: '@@ -0,0 +1,2 @@\n+one\n+two' },
        }),
      },
    })
    expect(created.findAll('.dr.add')).toHaveLength(0)
    expect(created.text()).toContain('one')
  })
})

/**
 * A call parked on the reader (plan 138): the panel becomes the gate's facts,
 * the footer below holds the question and its answers, the frame takes the
 * brand's border and the fold cannot hide it. A settled refusal draws one
 * sentence plus the user's own words under the arrow.
 */
describe('ToolCallCard gates', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => setLocale('en'))

  function gateAction(): ActionRow {
    return {
      id: 'act-rm',
      kind: 'shell',
      status: 'pending',
      payloadJson: JSON.stringify({ command: 'rm -rf build/', dangerous_rule: 'forced_rm' }),
      createdAt: 1,
      updatedAt: 1,
      permissionSuggestion: {
        permissionToolName: 'Bash',
        permissionInput: 'rm -rf build/',
        availableDecisions: ['accept', 'cancel'],
      },
    } as ActionRow
  }

  function mountGated(step: Partial<SubagentToolStep> = {}) {
    const store = usePendingActions({ sessionId: ref('s1'), version: ref(0), contextUsedPercent: ref(null) })
    store.pendingApprovals.value = [gateAction()]
    const wrapper = mount(ToolCallCard, {
      props: {
        open: false,
        step: stepOf({ toolName: 'shell', summary: 'ran rm -rf build/', status: 'awaiting approval', ...step }),
        pendingAction: gateAction(),
      },
      global: { provide: { [PendingActionsKey]: store } },
    })
    return wrapper
  }

  it('hangs the gate on the waiting call: facts panel, decision footer, brand frame', async () => {
    const wrapper = mountGated()
    await flushPromises()

    // The panel is the gate's facts, not a result the call never produced.
    expect(wrapper.find('[data-testid="approval-gate-fact"]').text()).toContain('强制删除文件')
    // The footer below holds the question and the engine's decisions.
    const footer = wrapper.get('[data-testid="approval-gate-shell"]')
    expect(footer.text()).toContain('要运行这条命令吗？')
    expect(footer.get('[data-testid="pending-decision-cancel"]').text()).toBe('不要运行')
    // The waiting frame is the brand's border, and the card stays open however
    // the caller left the fold.
    expect(wrapper.get('.tool-card').classes()).toContain('tool-card--waiting')
    expect(wrapper.get('[data-slot="collapsible-trigger"]').attributes('aria-expanded')).toBe('true')
    wrapper.unmount()
  })

  it('keeps the ordinary card for a call with no request parked on it', async () => {
    const wrapper = mount(ToolCallCard, {
      props: { open: true, scope: { sessionId: 's1' }, step: stepOf() },
    })
    expect(wrapper.get('.tool-card').classes()).not.toContain('tool-card--waiting')
    expect(wrapper.find('.tool-card-gate').exists()).toBe(false)
    wrapper.unmount()
  })

  it('settles a refusal as one sentence plus the user\'s words', () => {
    setLocale('zh')
    const command = mount(ToolCallCard, {
      props: {
        open: true,
        step: stepOf({ toolName: 'shell', summary: 'ran rm -rf build/', status: 'denied', denyFeedback: '改成移到 trash/' }),
      },
    })
    const refused = command.get('[data-testid="tool-refused"]')
    expect(refused.find('.say').text()).toBe('你拒绝了这条命令，它没有运行。')
    expect(refused.find('.said').text()).toContain('改成移到 trash/')

    const file = mount(ToolCallCard, {
      props: {
        open: true,
        step: stepOf({ toolName: 'write_file', summary: 'write api.ts', status: 'denied', denyFeedback: '先别新建' }),
      },
    })
    expect(file.get('[data-testid="tool-refused"] .say').text()).toBe('你拒绝了，文件没有写入。')

    const other = mount(ToolCallCard, {
      props: {
        open: true,
        step: stepOf({ toolName: 'web_search', summary: 'search', status: 'denied' }),
      },
    })
    expect(other.get('[data-testid="tool-refused"] .say').text()).toBe('你拒绝了这次调用，它没有执行。')
    expect(other.find('[data-testid="tool-refused"] .said').exists()).toBe(false)
  })
})

/**
 * The turn's deliveries are drawn once, in the group under its last words
 * (plan 153 A7, owner 2026-10-27). A settled present_files card is its head
 * alone: no file rows of its own, and no engine body either — that body is the
 * same per-file list, and two accounts of one delivery is what the rule
 * forbids. A failed call still says what failed.
 */
describe('ToolCallCard deliverables', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => setLocale('zh'))

  const presented = {
    presented: [
      { id: 'a1', name: 'Q3 经营分析报告.docx', relPath: 'Q3 经营分析报告.docx', title: 'Q3 经营分析报告', kind: 'document', preview: 'docx', size: 253952 },
      { id: 'a2', name: 'Q3 分渠道毛利.xlsx', relPath: 'Q3 分渠道毛利.xlsx', title: 'Q3 分渠道毛利', kind: 'spreadsheet', preview: 'sheet', size: 80896 },
    ],
    note: 'The user now sees a download and preview card for each file.',
  }

  /** The engine's own body for that card: one Markdown line per file. */
  const displayBody = '- **Q3 经营分析报告** · document · 248.0 KB · `Q3 经营分析报告.docx`\n- **Q3 分渠道毛利** · spreadsheet · 79.0 KB · `Q3 分渠道毛利.xlsx`'

  it('draws a settled present_files card as its head alone', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        scope: { sessionId: 's1' },
        step: stepOf({
          toolName: 'present_files',
          summary: 'presented files',
          header: 'Presented files · Q3 经营分析报告.docx',
          headerParts: { action: '已交付文件', target: 'Q3 经营分析报告.docx' },
          output: displayBody,
          status: 'completed',
          result: presented,
        }),
      },
    })

    // The head says what the call did.
    expect(wrapper.get('.ttl').text()).toContain('已交付文件')
    // No file rows and no engine body: the delivery group is the one place.
    expect(wrapper.find('[data-testid="artifact-list"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="artifact-card"]').exists()).toBe(false)
    expect(wrapper.find('.tool-card-body').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Q3 分渠道毛利.xlsx')
    wrapper.unmount()
  })

  it('still says what failed when the call did not deliver', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        open: true,
        step: stepOf({
          toolName: 'present_files',
          summary: 'present files',
          header: 'Failed to present files',
          status: 'failed',
          error: 'report.docx is outside the project directory',
        }),
      },
    })

    expect(wrapper.get('.tool-card-body').text()).toContain('report.docx is outside the project directory')
    wrapper.unmount()
  })

  it('opens the kept-plan exit by default, and only that exit', () => {
    // No `open` prop: the card decides, as the conversation's own call site
    // hands every card `default-open="false"`.
    const kept = mount(ToolCallCard, {
      props: {
        step: stepOf({
          toolName: 'exit_plan_mode',
          summary: 'kept the plan',
          header: 'Kept the plan',
          status: 'completed',
          planOnly: true,
          output: 'Kept the plan as the deliverable: plans/年度审计计划.md',
        }),
      },
    })

    // The prototype draws this card open (:4842): the facts line is the
    // answer, and a folded card leaves it outside the DOM in the browser.
    const sentence = kept.get('[data-testid="tool-kept-plan"]')
    expect(sentence.text()).toBe('已保留计划，还没有开始执行。')
    expect(kept.get('.tool-card').attributes('data-state')).toBe('open')
    kept.unmount()

    // An ordinary exit answers nothing: its card stays folded like any call's.
    const exited = mount(ToolCallCard, {
      props: {
        step: stepOf({
          toolName: 'exit_plan_mode',
          summary: 'exited plan mode',
          header: 'Exited plan mode',
          status: 'completed',
          output: 'Left plan mode.',
        }),
      },
    })
    expect(exited.get('.tool-card').attributes('data-state')).toBe('closed')
    exited.unmount()
  })
})
