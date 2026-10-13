import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { nextTick } from 'vue'

import { setLocale } from '@/locales'
import { forebrainApi, type ProjectRecord } from '@/lib/api'
import { useTopbarContent } from '@/composables/useTopbar'

// The stream the component talks to, filled in by the module mock below and
// driven by the test. useChatStream is the only module replaced: the
// conversation's send path and every subagent-view call land on these spies.
const fake = vi.hoisted(() => ({} as Record<string, unknown>))

vi.mock('@/composables/useChatStream', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/composables/useChatStream')>()
  const { ref } = await import('vue')
  const f = fake as {
    sessionId: unknown
    messages: unknown
    subagents: unknown
    subagentPendingInput: unknown
    subagentReturnedDraft: unknown
    withdrawSubagentInput: unknown
    interruptSubagentToSend: unknown
    sendToSubagent: unknown
    recallSubagentInput: unknown
    loadSubagentBudget: unknown
    takeSubagentReturnedDraft: unknown
    autoContinueForView: unknown
    cancelAutoContinue: unknown
    send: unknown
  }
  f.sessionId = ref('s1')
  f.messages = ref([])
  f.subagents = ref([])
  f.subagentPendingInput = ref({})
  f.subagentReturnedDraft = ref({})
  f.withdrawSubagentInput = vi.fn()
  f.interruptSubagentToSend = vi.fn()
  f.sendToSubagent = vi.fn()
  f.recallSubagentInput = vi.fn()
  f.loadSubagentBudget = vi.fn()
  f.send = vi.fn()
  f.takeSubagentReturnedDraft = vi.fn(() => null)
  f.autoContinueForView = vi.fn(() => null)
  f.cancelAutoContinue = vi.fn()
  return {
    ...actual,
    useChatStream: () => ({
      sessionId: f.sessionId,
      mode: ref('agent'),
      modePhase: ref(''),
      plan: ref(null),
      answer: ref(''),
      isStreaming: ref(false),
      error: ref(null),
      messages: f.messages,
      choose: vi.fn(),
      historyLoading: ref(false),
      historyError: ref(null),
      subagents: f.subagents,
      subagentCallTasks: new Map(),
      subagentNow: ref(0),
      runtimeStatus: ref({ kind: 'idle', elapsedMs: 0 }),
      mcpStatus: ref(null),
      autoContinue: ref(null),
      subagentAutoContinue: ref({}),
      autoContinueForView: f.autoContinueForView,
      cancelAutoContinue: f.cancelAutoContinue,
      lspRecommendation: ref(null),
      contextSignals: ref({ compactVersion: 0, budgetVersion: 0, activeRunId: undefined }),
      pendingActionsVersion: ref(0),
      pendingInputPreview: ref({ pendingSteers: [], rejectedSteers: [], queuedMessages: [] }),
      subagentPendingInput: f.subagentPendingInput,
      subagentReturnedDraft: f.subagentReturnedDraft,
      takeSubagentReturnedDraft: f.takeSubagentReturnedDraft,
      sendToSubagent: f.sendToSubagent,
      recallSubagentInput: f.recallSubagentInput,
      interruptSubagentToSend: f.interruptSubagentToSend,
      withdrawSubagentInput: f.withdrawSubagentInput,
      compactSubagent: vi.fn(),
      loadSubagentContext: vi.fn(),
      loadSubagentBudget: f.loadSubagentBudget,
      returnedDraft: ref(null),
      takeReturnedDraft: vi.fn(() => null),
      takeReturnedNotice: vi.fn(() => null),
      takeReturnedNoticeCode: vi.fn(() => null),
      send: f.send,
      editLastQueuedMessage: vi.fn(),
      sendPendingSteersAfterInterrupt: vi.fn(),
      loadMessages: vi.fn(),
      switchToSession: vi.fn(),
      formatRuntimeStatusLabel: actual.formatRuntimeStatusLabel,
    }),
  }
})

import ChatView from './ChatView.vue'

function makeRouter() {
  return createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div />' } }] })
}

// Shared by every describe below: jsdom has no ResizeObserver or matchMedia,
// and the thread and the workbench breakpoint need both.
beforeAll(() => {
  setLocale('en')
  ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  ;(window as unknown as { matchMedia: unknown }).matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })
})

async function mountView() {
  const router = makeRouter()
  router.push('/')
  await router.isReady()
  return mount(ChatView, {
    global: {
      plugins: [router],
      stubs: {
        Conversation: { template: '<div><slot /></div>' },
        ConversationContent: { template: '<div><slot /></div>' },
        ConversationScrollButton: true,
        ConversationEmptyState: true,
        SubagentConversation: true,
        ChatWorkbench: true,
        PendingActionsPanel: true,
        ContextDebugPanel: true,
        ApprovalPresetPicker: true,
        SessionHeartbeat: true,
        LspRecommendationCard: true,
        AutoContinueBanner: true,
        PendingInputQueuePopover: true,
        SlashPickerCard: true,
        PlanUpdateCard: true,
        ToolCallCard: true,
        ApprovalCard: true,
        SubagentCallCard: true,
        RunWorkedLine: true,
        RunErrorBlock: true,
        CompactionCard: true,
        GoalLine: true,
      },
    },
  })
}

describe('ChatView subagent view', () => {
  afterEach(() => {
    vi.clearAllMocks()
    ;(fake as { subagents: { value: unknown } }).subagents.value = []
  })

  function openSubagentView(wrapper: Awaited<ReturnType<typeof mountView>>, agentId: string) {
    const tab = wrapper.findAll('.forebrain-agent-tab')
      .find((node) => (node.attributes('title') ?? '') === agentId)
    tab?.trigger('click')
  }

  function seedSubagent() {
    ;(fake as { subagents: { value: unknown[] } }).subagents.value = [
      { agentId: 'task-1', agentType: 'general-purpose', task: 'answer', status: 'running', blocks: [], inputTokens: 0, outputTokens: 0, updatedSeq: 1 },
    ]
  }

  it('takes a just-sent message back on Escape, staying in the view', async () => {
    seedSubagent()
    ;(fake as { withdrawSubagentInput: { mockResolvedValue: (v: unknown) => void } }).withdrawSubagentInput
      .mockResolvedValue([{ text: 'continue', attachments: [], mentionImages: [] }])
    ;(fake as { interruptSubagentToSend: { mockResolvedValue: (v: unknown) => void } }).interruptSubagentToSend
      .mockResolvedValue(false)
    const wrapper = await mountView()
    openSubagentView(wrapper, 'task-1')
    await nextTick()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    await nextTick()
    expect(fake.withdrawSubagentInput).toHaveBeenCalledWith('task-1')
    expect(fake.interruptSubagentToSend).not.toHaveBeenCalled()
    // Still in the view: the subagent's conversation is the one drawn.
    expect(wrapper.find('subagent-conversation-stub').exists()).toBe(true)
    wrapper.unmount()
  })

  it('interrupts to send on Escape when nothing can be withdrawn', async () => {
    seedSubagent()
    ;(fake as { withdrawSubagentInput: { mockResolvedValue: (v: unknown) => void } }).withdrawSubagentInput
      .mockResolvedValue([])
    ;(fake as { interruptSubagentToSend: { mockResolvedValue: (v: unknown) => void } }).interruptSubagentToSend
      .mockResolvedValue(true)
    const wrapper = await mountView()
    openSubagentView(wrapper, 'task-1')
    await nextTick()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    await nextTick()
    expect(fake.interruptSubagentToSend).toHaveBeenCalledWith('task-1')
    wrapper.unmount()
  })

  it('shows the active view’s own auto-continue banner and cancels it by that agent', async () => {
    seedSubagent()
    ;(fake as { autoContinueForView: { mockImplementation: (fn: (v: string) => unknown) => void } }).autoContinueForView
      .mockImplementation((view: string) => (view === 'task-1' ? { continueAt: '2026-09-30T02:10:05Z', code: 'rate_limit_quota' } : null))
    const wrapper = await mountView()
    const banner = () => wrapper.findComponent({ name: 'AutoContinueBanner' })
    expect(banner().props('state')).toBeNull()
    openSubagentView(wrapper, 'task-1')
    await nextTick()
    expect(banner().props('state')).toMatchObject({ code: 'rate_limit_quota' })
    banner().vm.$emit('cancel')
    await nextTick()
    expect(fake.cancelAutoContinue).toHaveBeenCalledWith('task-1')
    wrapper.unmount()
  })

  it('leaves for the conversation on Escape when nothing is in flight', async () => {
    seedSubagent()
    ;(fake as { withdrawSubagentInput: { mockResolvedValue: (v: unknown) => void } }).withdrawSubagentInput
      .mockResolvedValue([])
    ;(fake as { interruptSubagentToSend: { mockResolvedValue: (v: unknown) => void } }).interruptSubagentToSend
      .mockResolvedValue(false)
    const wrapper = await mountView()
    openSubagentView(wrapper, 'task-1')
    await nextTick()
    expect(wrapper.find('subagent-conversation-stub').exists()).toBe(true)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    // Back on the conversation: the subagent's view is gone.
    await vi.waitFor(() => {
      expect(wrapper.find('subagent-conversation-stub').exists()).toBe(false)
    })
    wrapper.unmount()
  })

  it('keeps each view’s composer draft to that view', async () => {
    seedSubagent()
    ;(fake as { withdrawSubagentInput: { mockResolvedValue: (v: unknown) => void } }).withdrawSubagentInput.mockResolvedValue([])
    ;(fake as { interruptSubagentToSend: { mockResolvedValue: (v: unknown) => void } }).interruptSubagentToSend.mockResolvedValue(false)
    const slash = vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({ surface: 'webchat', query: '', options: { duringRun: false, sideConversation: false }, records: [] })
    try {
      const wrapper = await mountView()
      const composer = wrapper.find('textarea')
      await composer.setValue('a note for the conversation')

      // The subagent's view opens with its own (empty) draft.
      wrapper.findAll('.forebrain-agent-tab')[1].trigger('click')
      await nextTick()
      await nextTick()
      expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('')
      await wrapper.find('textarea').setValue('a note for the subagent')

      // Back on the conversation: its draft is there, the subagent's is kept.
      wrapper.findAll('.forebrain-agent-tab')[0].trigger('click')
      await nextTick()
      await nextTick()
      expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('a note for the conversation')
      wrapper.findAll('.forebrain-agent-tab')[1].trigger('click')
      await nextTick()
      await nextTick()
      expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('a note for the subagent')
      wrapper.unmount()
    } finally {
      slash.mockRestore()
    }
  })
})

describe('ChatView conversation bubbles', () => {
  afterEach(() => {
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
  })

  it('shows a turn’s words when its blocks draw nothing', async () => {
    ;(fake as { messages: { value: unknown[] } }).messages.value = [
      { id: 'u1', role: 'user', content: 'submit the plan' },
      { id: 'a1', role: 'assistant', content: 'Request cancelled', blocks: [{ kind: 'gate', id: 'gate:call-a', stepId: 'call-a' }] },
      { id: 'u2', role: 'user', content: 'try again' },
      { id: 'a2', role: 'assistant', content: 'here is the plan', blocks: [
        { kind: 'assistant', text: 'here is the plan', open: false },
        { kind: 'gate', id: 'gate:call-b', stepId: 'call-b' },
      ] },
    ]
    const wrapper = await mountView()
    // The Markdown renderer types its content out rather than painting it at
    // once (see ToolOutput.test.ts), so the words land a tick or more after
    // mount.
    await vi.waitFor(() => {
      expect(wrapper.text()).toContain('Request cancelled')     // only a gate anchor: the turn shows its own text
      expect(wrapper.text()).toContain('here is the plan')
    })
    const text = wrapper.text()
    expect(text.split('here is the plan').length - 1).toBe(1)   // a visible block: drawn once, not duplicated by content
    wrapper.unmount()
  })

  it('labels a thinking block with the model’s thinking time', async () => {
    setLocale('en')
    ;(fake as { messages: { value: unknown[] } }).messages.value = [
      { id: 'u1', role: 'user', content: 'think about it' },
      { id: 'a1', role: 'assistant', content: 'here is the answer', blocks: [
        { kind: 'thinking', text: 'weighing the options', open: false, startedAt: 0, durationMs: 12000 },
        { kind: 'assistant', text: 'here is the answer', open: false },
      ] },
    ]
    const wrapper = await mountView()
    await vi.waitFor(() => {
      expect(wrapper.text()).toContain('Thought for 12s')   // the same label the subagent view shows
      expect(wrapper.text()).toContain('weighing the options')
    })
    wrapper.unmount()
  })
})

/**
 * The new-conversation page (plan 029): a chat can only be opened into a
 * project, an unknown directory is trusted once before it is used, a created
 * project is announced, and a directory that is gone is refused with its own
 * sentence.
 */
describe('ChatView new conversation picks a project first', () => {
  const sessionIdRef = () => (fake as { sessionId: { value: string | null } }).sessionId
  const messagesRef = () => (fake as { messages: { value: unknown[] } }).messages
  const sendMock = () => (fake as { send: { mock: { calls: unknown[][] }; mockClear: () => void } }).send

  function project(over: Partial<ProjectRecord>): ProjectRecord {
    return {
      id: 'p', name: 'p', icon: '', description: '', instructions: '', root: '/p', projectKey: '',
      memoryScope: 'shared', resourceAccess: true, pinned: false, createdAt: 0, updatedAt: 0, ...over,
    }
  }

  afterEach(() => {
    sessionIdRef().value = 's1'
    messagesRef().value = []
    sendMock().mockClear()
    vi.restoreAllMocks()
  })

  async function openNewChat(projects: ProjectRecord[]) {
    localStorage.clear()
    sessionIdRef().value = null
    messagesRef().value = []
    vi.spyOn(forebrainApi, 'projectsList').mockResolvedValue(projects)
    vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({ surface: 'webchat', query: '', options: { duringRun: false, sideConversation: false }, records: [] })
    const wrapper = await mountView()
    await flushPromises()
    return wrapper
  }

  async function pickProject(wrapper: Awaited<ReturnType<typeof openNewChat>>, name: string) {
    await wrapper.find('[data-testid="project-pill"]').trigger('click')
    await nextTick()
    const option = wrapper.findAll('.pj-opt').find((row) => row.text().includes(name))!
    await option.trigger('click')
    await nextTick()
  }

  async function submit(wrapper: Awaited<ReturnType<typeof openNewChat>>, text: string) {
    await wrapper.find('textarea').setValue(text)
    await wrapper.find('textarea').trigger('keydown', { key: 'Enter' })
    await flushPromises()
  }

  it('shows the greeting and the starters for a brand-new chat', async () => {
    const wrapper = await openNewChat([])
    expect(wrapper.find('[data-testid="chat-greeting"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="chat-starters"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="project-pill"]').length).toBe(1)
    wrapper.unmount()
  })

  it('refuses to send with no project picked', async () => {
    const create = vi.spyOn(forebrainApi, 'chatSessionCreate')
    const wrapper = await openNewChat([])
    await submit(wrapper, 'hello')
    expect(create).not.toHaveBeenCalled()
    expect(sendMock()).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="project-pill"]').attributes('aria-invalid')).toBe('true')
    wrapper.unmount()
  })

  it('asks about trust once, and sends only after it is given', async () => {
    const alpha = project({ id: 'a', name: 'alpha', root: '/work/alpha', trustLevel: 'unknown' })
    const create = vi.spyOn(forebrainApi, 'chatSessionCreate').mockResolvedValue({ id: 'new-1', title: '' })
    const trust = vi.spyOn(forebrainApi, 'hostTrustSet').mockResolvedValue({ ok: true, level: 'trusted' })
    const wrapper = await openNewChat([alpha])
    await pickProject(wrapper, 'alpha')

    await submit(wrapper, 'hello')
    // The prompt is up and nothing was sent.
    expect(wrapper.find('.trust-path').exists()).toBe(true)
    expect(create).not.toHaveBeenCalled()
    // Cancel keeps the message unsent.
    await wrapper.findAll('.modal-f .btn')[0].trigger('click')
    await flushPromises()
    expect(create).not.toHaveBeenCalled()
    expect(sendMock()).not.toHaveBeenCalled()

    await submit(wrapper, 'hello again')
    await wrapper.findAll('.modal-f .btn')[1].trigger('click')
    await flushPromises()
    expect(trust).toHaveBeenCalledWith('/work/alpha', true)
    expect(create).toHaveBeenCalledWith({ projectId: 'a' })
    expect(sendMock()).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('announces the project it created for a new folder', async () => {
    const folder = project({ id: 'a', name: 'alpha', root: '/work/alpha', trustLevel: 'trusted' })
    vi.spyOn(forebrainApi, 'chatSessionCreate').mockResolvedValue({
      id: 'new-1', title: '', created: true, project: folder,
    })
    const wrapper = await openNewChat([folder])
    await pickProject(wrapper, 'alpha')
    await submit(wrapper, 'hello')
    expect(wrapper.find('[data-testid="created-project-line"]').exists()).toBe(true)
    expect(sendMock()).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('reports a directory that is gone with its own sentence', async () => {
    const gone = project({ id: 'a', name: 'alpha', root: '/work/alpha', trustLevel: 'trusted' })
    vi.spyOn(forebrainApi, 'chatSessionCreate').mockRejectedValue(new Error('session_directory_missing'))
    const wrapper = await openNewChat([gone])
    await pickProject(wrapper, 'alpha')
    await submit(wrapper, 'hello')
    expect(sendMock()).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('/work/alpha')
    expect(wrapper.text()).toContain('no longer exists')
    wrapper.unmount()
  })
})

/**
 * The conversation's own row in the shell's top bar (plan 016 step 1): the
 * page fills the shared slot with its title, project tag, model, heartbeat
 * and workbench toggle — and a conversation that does not exist yet carries
 * no heartbeat, because there is nothing for it to ask again.
 */
describe('ChatView fills the shell top bar', () => {
  const sessionIdRef = () => (fake as { sessionId: { value: string | null } }).sessionId

  function topbarNodes() {
    const content = useTopbarContent().value
    expect(content).not.toBeNull()
    return [...(content?.leading?.() ?? []), ...(content?.trailing?.() ?? [])]
  }

  function isHeartbeat(node: { type: unknown }): boolean {
    const type = node.type as { name?: string; __name?: string } | string
    return typeof type === 'string'
      ? false
      : Boolean(type?.name === 'SessionHeartbeat' || type?.__name === 'SessionHeartbeat')
  }

  it('carries the title, the workbench toggle and the heartbeat of an open conversation', async () => {
    sessionIdRef().value = 's1'
    const wrapper = await mountView()
    const nodes = topbarNodes()
    expect(nodes.some((node) => node.props?.['data-testid'] === 'chat-session-title')).toBe(true)
    expect(nodes.some((node) => node.props?.['data-testid'] === 'workbench-toggle')).toBe(true)
    // The heartbeat travels with the conversation's row.
    expect(nodes.some(isHeartbeat)).toBe(true)
    wrapper.unmount()
  })

  it('carries no heartbeat for a conversation that does not exist yet', async () => {
    sessionIdRef().value = null
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
    const wrapper = await mountView()
    const nodes = topbarNodes()
    expect(nodes.some((node) => node.props?.['data-testid'] === 'chat-session-title')).toBe(true)
    expect(nodes.some(isHeartbeat)).toBe(false)
    wrapper.unmount()
    sessionIdRef().value = 's1'
  })
})

describe('ChatView rewind to a message (plan 033)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.clearAllMocks()
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
  })

  function seedThread() {
    ;(fake as { messages: { value: unknown[] } }).messages.value = [
      { id: 'u1', role: 'user', content: 'rewind to me please', rowId: 11 },
      { id: 'a1', role: 'assistant', content: 'answered' },
    ]
  }

  it('offers the rewind and copy actions on a user message', async () => {
    seedThread()
    const wrapper = await mountView()
    await nextTick()
    const rewind = wrapper.find('[data-testid="message-rewind"]')
    expect(rewind.exists()).toBe(true)
    expect(wrapper.find('[data-testid="message-copy"]').exists()).toBe(true)
    expect((rewind.element as HTMLButtonElement).disabled).toBe(false)
    wrapper.unmount()
  })

  it('opens the dialog, restores, and leaves the undo line', async () => {
    seedThread()
    vi.spyOn(forebrainApi, 'rewindPreview').mockResolvedValue({
      anchorMessageId: 11,
      anchorText: 'rewind to me please',
      files: [{ path: 'pkg/a.go', added: 2, removed: 1, changedAfter: false, created: false }],
    })
    vi.spyOn(forebrainApi, 'rewind').mockResolvedValue({ anchorText: 'rewind to me please', mode: 'both', restored: ['pkg/a.go'] })
    const wrapper = await mountView()
    await nextTick()
    await wrapper.find('[data-testid="message-rewind"]').trigger('click')
    await flushPromises()
    // The dialog shows the message and the three modes.
    expect(wrapper.text()).toContain('Code and conversation')
    expect(wrapper.text()).toContain('Conversation only')
    expect(wrapper.text()).toContain('Code only')
    expect(wrapper.find('.rw-files').exists()).toBe(true)

    await wrapper.find('[data-testid="rewind-restore"]').trigger('click')
    await flushPromises()
    await nextTick()
    expect(forebrainApi.rewind).toHaveBeenCalledWith('s1', expect.objectContaining({ messageId: 11, mode: 'both' }))
    expect(wrapper.find('[data-testid="rewind-undo-line"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('hides the file list in conversation-only mode', async () => {
    seedThread()
    vi.spyOn(forebrainApi, 'rewindPreview').mockResolvedValue({
      anchorMessageId: 11,
      anchorText: 'rewind to me please',
      files: [{ path: 'pkg/a.go', added: 1, removed: 0, changedAfter: true, created: false }],
    })
    const wrapper = await mountView()
    await nextTick()
    await wrapper.find('[data-testid="message-rewind"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('.rw-files').exists()).toBe(true)
    const chatOnly = wrapper.findAll('.rw-modes .rc').find((b) => b.text().includes('Conversation only'))
    await chatOnly!.trigger('click')
    await nextTick()
    expect(wrapper.find('.rw-files').exists()).toBe(false)
    wrapper.unmount()
  })
})
