import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount } from '@vue/test-utils'
import tip from '@/directives/tip'
import { createRouter, createMemoryHistory } from 'vue-router'
import { nextTick } from 'vue'

import { setLocale } from '@/locales'
import { forebrainApi, type ProjectRecord } from '@/lib/api'
import { useTopbarContent } from '@/composables/useTopbar'
import { persistLastSessionId } from '@/composables/useLastSession'
import { useWorkbench } from '@/composables/useWorkbench'

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
    resetForNewChat: unknown
    send: unknown
    runtimeStatus: unknown
  }
  f.sessionId = ref('s1')
  f.messages = ref([])
  f.subagents = ref([])
  f.subagentPendingInput = ref({})
  f.subagentReturnedDraft = ref({})
  f.runtimeStatus = ref({ kind: 'idle', elapsedMs: 0 })
  f.withdrawSubagentInput = vi.fn()
  f.interruptSubagentToSend = vi.fn()
  f.sendToSubagent = vi.fn()
  f.recallSubagentInput = vi.fn()
  f.loadSubagentBudget = vi.fn()
  f.send = vi.fn()
  f.takeSubagentReturnedDraft = vi.fn(() => null)
  f.autoContinueForView = vi.fn(() => null)
  f.cancelAutoContinue = vi.fn()
  f.resetForNewChat = vi.fn()
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
      runtimeStatus: f.runtimeStatus,
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
      resetForNewChat: f.resetForNewChat,
      formatRuntimeStatusLabel: actual.formatRuntimeStatusLabel,
    }),
  }
})

import ChatView from './ChatView.vue'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

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

async function mountView(extraStubs: Record<string, unknown> = {}, router = makeRouter(), location = '/') {
  router.push(location)
  await router.isReady()
  return mount(ChatView, {
    global: {
      plugins: [router],
      stubs: {
        Conversation: { template: '<div><slot /></div>' },
        ConversationContent: { template: '<div><slot /></div>' },
        ConversationScrollButton: true,
        ConversationEmptyState: true,
        SubagentTab: true,
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
        ...extraStubs,
      },
    },
  })
}

describe('ChatView subagent drawer', () => {
  afterEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    ;(fake as { subagents: { value: unknown } }).subagents.value = []
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
    useWorkbench().open.value = false
  })

  const subagentsRef = () => (fake as { subagents: { value: unknown[] } }).subagents
  const messagesRef = () => (fake as { messages: { value: unknown[] } }).messages

  function seedSubagent(over: Record<string, unknown> = {}) {
    subagentsRef().value = [
      {
        agentId: 'task-1',
        agentType: 'general-purpose',
        title: 'read the plan',
        task: 'answer',
        status: 'running',
        blocks: [],
        inputTokens: 0,
        outputTokens: 0,
        updatedSeq: 1,
        ...over,
      },
    ]
  }

  /** A thread holding the subagent_* card the drawer is opened from. */
  function seedCard() {
    messagesRef().value = [
      { id: 'u1', role: 'user', content: 'dispatch it' },
      {
        id: 'a1',
        role: 'assistant',
        content: '',
        blocks: [
          { kind: 'subagent', step: { stepId: 'call-1', toolName: 'subagent_dispatch', summary: 'dispatch' } },
        ],
      },
    ]
  }

  async function openDrawer(wrapper: Awaited<ReturnType<typeof mountView>>, agentId = 'task-1') {
    await wrapper.findComponent({ name: 'SubagentCallCard' }).vm.$emit('open', agentId)
    await nextTick()
    await nextTick()
  }

  const drawer = (wrapper: Awaited<ReturnType<typeof mountView>>) => wrapper.find('[data-testid="subagent-drawer"]')
  const tabs = (wrapper: Awaited<ReturnType<typeof mountView>>) => wrapper.findAllComponents({ name: 'SubagentTab' })

  type TopbarNode = { props?: Record<string, unknown> | null; children?: unknown }

  /** A top-bar button's children, as the render functions build them. */
  function childrenOf(node?: TopbarNode): TopbarNode[] {
    return Array.isArray(node?.children) ? (node.children as TopbarNode[]) : []
  }

  it('opens the subagent beside the conversation, and gives up the column to the workbench', async () => {
    seedSubagent()
    seedCard()
    const workbench = useWorkbench()
    const wrapper = await mountView()
    await nextTick()
    expect(drawer(wrapper).exists()).toBe(false)

    // The workbench holds the column first; opening the subagent takes it.
    workbench.open.value = true
    await nextTick()
    await openDrawer(wrapper)
    expect(drawer(wrapper).exists()).toBe(true)
    expect(tabs(wrapper).length).toBe(1)
    expect(tabs(wrapper)[0].props('record')).toMatchObject({ agentId: 'task-1' })
    expect(workbench.open.value).toBe(false)

    // …and opening the workbench puts the drawer away.
    workbench.open.value = true
    await nextTick()
    expect(drawer(wrapper).exists()).toBe(false)
    wrapper.unmount()
  })

  it('takes a just-sent message back into the tab’s composer on Escape, keeping the drawer', async () => {
    seedSubagent()
    seedCard()
    const restore = vi.fn()
    const withdraw = (fake as { withdrawSubagentInput: { mockResolvedValue: (v: unknown) => void } }).withdrawSubagentInput
    withdraw.mockResolvedValue([{ text: 'continue', attachments: [], mentionImages: [] }])
    ;(fake as { interruptSubagentToSend: { mockResolvedValue: (v: unknown) => void } }).interruptSubagentToSend
      .mockResolvedValue(false)
    const wrapper = await mountView({
      SubagentTab: { name: 'SubagentTab', template: '<div class="subagent-tab-stub" />', methods: { restoreSubmission: restore } },
    })
    await nextTick()
    await openDrawer(wrapper)

    await tabs(wrapper)[0].vm.$emit('escape-key')
    await nextTick()
    await nextTick()
    expect(fake.withdrawSubagentInput).toHaveBeenCalledWith('task-1')
    // Nothing was left to interrupt: the withdrawn message is the draft again.
    expect(fake.interruptSubagentToSend).not.toHaveBeenCalled()
    expect(restore).toHaveBeenCalledWith({ text: 'continue', attachments: [], mentionImages: [] })
    expect(drawer(wrapper).exists()).toBe(true)
    wrapper.unmount()
  })

  it('collapses the drawer on Escape when there is nothing to take back or interrupt', async () => {
    seedSubagent()
    seedCard()
    ;(fake as { withdrawSubagentInput: { mockResolvedValue: (v: unknown) => void } }).withdrawSubagentInput
      .mockResolvedValue([])
    ;(fake as { interruptSubagentToSend: { mockResolvedValue: (v: unknown) => void } }).interruptSubagentToSend
      .mockResolvedValue(false)
    const wrapper = await mountView()
    await nextTick()
    await openDrawer(wrapper)
    expect(drawer(wrapper).exists()).toBe(true)

    await tabs(wrapper)[0].vm.$emit('escape-key')
    await vi.waitFor(() => expect(drawer(wrapper).exists()).toBe(false))
    // The subagent is still running: a shut drawer is not a stop.
    expect(subagentsRef().value[0]).toMatchObject({ status: 'running' })
    wrapper.unmount()
  })

  it('gives a tab its own continuation and cancels it by that agent', async () => {
    seedSubagent()
    seedCard()
    ;(fake as { autoContinueForView: { mockImplementation: (fn: (v: string) => unknown) => void } }).autoContinueForView
      .mockImplementation((view: string) => (view === 'task-1' ? { continueAt: '2026-09-30T02:10:05Z', code: 'rate_limit_quota' } : null))
    const wrapper = await mountView()
    await nextTick()
    await openDrawer(wrapper)

    const tab = tabs(wrapper)[0]
    expect(tab.props('autoContinue')).toMatchObject({ code: 'rate_limit_quota' })
    await tab.vm.$emit('cancel-auto-continue')
    await nextTick()
    expect(fake.cancelAutoContinue).toHaveBeenCalledWith('task-1')
    wrapper.unmount()
  })

  it('sends the conversation’s own messages to the conversation, never to a subagent', async () => {
    seedSubagent()
    seedCard()
    const wrapper = await mountView()
    await nextTick()
    await openDrawer(wrapper)

    await wrapper.find('textarea').setValue('a note for the conversation')
    await wrapper.find('textarea').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(fake.sendToSubagent).not.toHaveBeenCalled()
    expect(fake.send).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('carries the drawer toggle with the running count, and no count on the workbench', async () => {
    seedSubagent()
    seedSubagent2()
    const wrapper = await mountView()
    await nextTick()
    const trailing = () => useTopbarContent().value?.trailing?.() ?? []
    const byTestId = (id: string) => trailing().find((node) => node.props?.['data-testid'] === id)
    const toggle = byTestId('subagent-drawer-toggle')
    expect(toggle).toBeTruthy()
    expect(toggle?.props?.['aria-pressed']).toBe('false')
    // Two dispatched, one still running.
    const counts = childrenOf(toggle).filter((child) => child.props?.class === 'forebrain-chip-count')
    expect(counts.length).toBe(1)
    expect(counts[0].children).toBe('1')
    // The workbench button no longer carries a number of its own.
    expect(childrenOf(byTestId('workbench-toggle')).some((child) => child.props?.class === 'forebrain-chip-count')).toBe(false)
    wrapper.unmount()
  })

  it('draws the real tab: its own composer, its working line and its stop', async () => {
    // Nothing about the tab is stubbed here: the drawer hands the record to
    // SubagentTab as it really is, and that component owns the rest.
    setLocale('en')
    seedSubagent()
    seedCard()
    const slash = vi.spyOn(forebrainApi, 'slashCommands')
      .mockResolvedValue({ surface: 'webchat', query: '', options: { duringRun: false, sideConversation: false }, records: [] })
    try {
      const wrapper = await mountView({ SubagentTab: false })
      await nextTick()
      await openDrawer(wrapper)

      // The tab's own composer, not the conversation's: the drawer holds the
      // textarea this subagent's messages are typed in.
      const drawer = wrapper.find('[data-testid="subagent-drawer"]')
      const textarea = drawer.find('textarea')
      expect(textarea.exists()).toBe(true)
      expect((textarea.element as HTMLTextAreaElement).placeholder)
        .toBe('Message this subagent — / for commands, @ to reference files')
      // Running: the terminal's working line sits above that composer, and the
      // tab offers its own stop.
      const working = drawer.find('[data-testid="subagent-working"]')
      expect(working.exists()).toBe(true)
      expect(working.text()).toContain('Working (')
      expect(drawer.find('[data-testid="subagent-stop"]').exists()).toBe(true)
      // The conversation's composer is still there, outside the drawer.
      expect(wrapper.findAll('textarea').length).toBe(2)
    } finally {
      slash.mockRestore()
    }
  })

  it('acts on one Escape once, though the tab and the drawer both hear it', async () => {
    setLocale('en')
    seedSubagent()
    seedCard()
    const slash = vi.spyOn(forebrainApi, 'slashCommands')
      .mockResolvedValue({ surface: 'webchat', query: '', options: { duringRun: false, sideConversation: false }, records: [] })
    ;(fake as { withdrawSubagentInput: { mockResolvedValue: (v: unknown) => void } }).withdrawSubagentInput
      .mockResolvedValue([{ text: 'continue', attachments: [], mentionImages: [] }])
    ;(fake as { interruptSubagentToSend: { mockResolvedValue: (v: unknown) => void } }).interruptSubagentToSend
      .mockResolvedValue(false)
    try {
      const wrapper = await mountView({ SubagentTab: false })
      await nextTick()
      await openDrawer(wrapper)

      // The keydown rises from the tab's own textarea through the tab (which
      // spends it) to the drawer (which must then stand down): one take-back,
      // and the drawer stays where it is.
      await wrapper.find('[data-testid="subagent-drawer"] textarea').trigger('keydown', { key: 'Escape' })
      await nextTick()
      await nextTick()
      expect(fake.withdrawSubagentInput).toHaveBeenCalledTimes(1)
      expect(fake.withdrawSubagentInput).toHaveBeenCalledWith('task-1')
      expect(drawer(wrapper).exists()).toBe(true)
    } finally {
      slash.mockRestore()
    }
  })

  it('opens the subagent a jump names in the address bar, then spends the parameter', async () => {
    seedSubagent()
    ;(fake as { sessionId: { value: string | null } }).sessionId.value = 's1'
    const router = makeRouter()
    const replace = vi.spyOn(router, 'replace')
    const wrapper = await mountView({}, router, '/?session=s1&agent=task-1')
    await flushPromises()
    expect(drawer(wrapper).exists()).toBe(true)
    expect(tabs(wrapper).length).toBe(1)
    expect(tabs(wrapper)[0].props('record')).toMatchObject({ agentId: 'task-1' })
    expect(replace).toHaveBeenCalled()
    const calls = replace.mock.calls
    const lastQuery = calls[calls.length - 1]?.[0] as { query: Record<string, string> }
    expect(lastQuery.query.agent).toBeUndefined()
    expect(lastQuery.query.session).toBe('s1')
    wrapper.unmount()
  })

  function seedSubagent2() {
    subagentsRef().value = [
      ...subagentsRef().value,
      { agentId: 'task-2', agentType: 'plan-reviewer', task: 'review', status: 'done', blocks: [], inputTokens: 0, outputTokens: 0, updatedSeq: 2 },
    ]
  }
})

/**
 * The run's own line above the composer (plan 078, step 7b): the terminal's
 * order — the state, then the checklist, then the first-token wait — and no
 * line at all while nothing runs.
 */
describe('ChatView run status line', () => {
  const runtimeStatus = () => (fake as { runtimeStatus: { value: unknown } }).runtimeStatus

  afterEach(() => {
    runtimeStatus().value = { kind: 'idle', elapsedMs: 0 }
    ;(fake as { sessionId: { value: string | null } }).sessionId.value = 's1'
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
  })

  it('reads the state, the checklist, then the first-token wait', async () => {
    setLocale('en')
    ;(fake as { messages: { value: unknown[] } }).messages.value = [
      { id: 'u1', role: 'user', content: 'fix it' },
    ]
    runtimeStatus().value = {
      kind: 'working',
      elapsedMs: 72_000,
      planCompleted: 3,
      planTotal: 5,
      planActive: 'fix the middleware',
      firstTokenMs: 1200,
    }
    const wrapper = await mountView()
    await flushPromises()
    const line = wrapper.find('[data-testid="run-status-line"]')
    expect(line.exists()).toBe(true)
    const text = line.text().replace(/\s+/g, ' ')
    expect(text).toContain('Working (1m 12s • esc to interrupt)')
    expect(text).toContain('3/5')
    expect(text).toContain('fix the middleware')
    expect(text).toContain('first token 1.2s')
    // The terminal puts the first-token wait last, after the checklist.
    expect(text.indexOf('3/5')).toBeLessThan(text.indexOf('first token 1.2s'))
    wrapper.unmount()
  })

  it('draws nothing while no run is under way', async () => {
    ;(fake as { messages: { value: unknown[] } }).messages.value = [
      { id: 'u1', role: 'user', content: 'fix it' },
    ]
    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="run-status-line"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('draws nothing on the new-conversation page', async () => {
    ;(fake as { sessionId: { value: string | null } }).sessionId.value = null
    runtimeStatus().value = { kind: 'working', elapsedMs: 3_000 }
    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="chat-greeting"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="run-status-line"]').exists()).toBe(false)
    wrapper.unmount()
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

  it('keeps the bubble to the user’s words: actions and attachments are not inside its chrome', async () => {
    setLocale('en')
    ;(fake as { messages: { value: unknown[] } }).messages.value = [
      {
        id: 'u1',
        role: 'user',
        content: '这个 401 是哪一步报的？',
        attachments: [{ path: 'shot.png', name: 'shot.png', mediaType: 'image/png' }],
      },
    ]
    const wrapper = await mountView()
    await flushPromises()

    // The bubble is `.u` alone. The hover actions sit under it on the page (a
    // hidden 24px strip inside the bubble is what made every user message read
    // looser than the design), and what the message attached sits in it.
    const bubble = wrapper.find('.u-wrap .u')
    expect(bubble.exists()).toBe(true)
    expect(bubble.text()).toContain('这个 401 是哪一步报的？')
    expect(bubble.find('ul.u-atts').exists()).toBe(true)
    expect(bubble.find('[data-testid="message-copy"]').exists()).toBe(false)
    expect(wrapper.find('.u-wrap > .u-acts [data-testid="message-copy"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('draws an image refusal under its message and opens the model menu from it', async () => {
    setLocale('en')
    const sessionModel = vi.spyOn(forebrainApi, 'sessionModel').mockResolvedValue({
      provider: 'deepseek',
      model: 'deepseek-v4-pro',
      effort: '',
      options: [
        { provider: 'deepseek', model: 'deepseek-v4-pro', label: 'DeepSeek V4 Pro', efforts: [], supportsImages: false },
        { provider: 'deepseek', model: 'deepseek-chat', label: 'DeepSeek Chat', efforts: [], supportsImages: true },
      ],
    })
    try {
      ;(fake as { messages: { value: unknown[] } }).messages.value = [
        {
          id: 'u1',
          role: 'user',
          content: '这个 401 是哪一步报的？\n[Image #1]',
          attachments: [{ path: 'shot.png', name: 'shot.png', mediaType: 'image/png' }],
        },
        { id: 'n1', role: 'notice', content: '', imageRefusal: { provider: 'deepseek', model: 'deepseek-v4-pro' } },
      ]
      const wrapper = await mountView()
      await flushPromises()

      const notice = wrapper.find('[data-testid="image-refused-notice"]')
      expect(notice.exists()).toBe(true)
      // The model name is bold and outside the sentence, exactly as the
      // design draws the block; the sentence itself is the viewer's language.
      expect(notice.find('b').text()).toBe('deepseek-v4-pro')
      expect(notice.text()).toContain('does not accept images. The message is kept and was not sent.')
      expect(notice.text()).toContain('This conversation has images. Switch to a model that accepts images, then continue.')
      // The message it refused is still in the thread with its picture.
      expect(wrapper.text()).toContain('这个 401 是哪一步报的？')
      expect(wrapper.text()).toContain('shot.png')

      // The way out is the composer's own model menu, opened from here.
      expect(wrapper.find('[data-testid="model-option-deepseek-chat"]').exists()).toBe(false)
      await wrapper.find('[data-testid="image-refused-switch"]').trigger('click')
      await nextTick()
      expect(wrapper.find('[data-testid="model-option-deepseek-chat"]').exists()).toBe(true)
      wrapper.unmount()
    } finally {
      sessionModel.mockRestore()
    }
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

  it('names an untitled conversation the way the engine will', async () => {
    // The server has not written the title yet: the bar already reads what the
    // first message will name it — first line, at most 50 characters (plan 072).
    sessionIdRef().value = 's1'
    ;(fake as { messages: { value: unknown[] } }).messages.value = [
      { id: 'u1', role: 'user', content: `${'a'.repeat(60)}\nsecond line` },
    ]
    const wrapper = await mountView()
    const title = (useTopbarContent().value?.leading?.() ?? [])
      .find((node) => node.props?.['data-testid'] === 'chat-session-title')
    expect(title?.children).toBe(`${'a'.repeat(50)}...`)
    wrapper.unmount()
  })

  it('carries nothing for a conversation that does not exist yet', async () => {
    // A new chat names nothing and has no third column to open: its top bar
    // starts empty and fills on the first message (plan 072).
    sessionIdRef().value = null
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
    const wrapper = await mountView()
    expect(useTopbarContent().value?.leading?.() ?? []).toEqual([])
    expect(topbarNodes()).toEqual([])
    wrapper.unmount()
    sessionIdRef().value = 's1'
  })
})

describe('ChatView opens on the new-conversation page', () => {
  const sessionIdRef = () => (fake as { sessionId: { value: string | null } }).sessionId
  const resetSpy = () => (fake as { resetForNewChat: ReturnType<typeof vi.fn> }).resetForNewChat

  beforeEach(() => resetSpy().mockClear())

  afterEach(() => {
    persistLastSessionId(null)
    sessionIdRef().value = 's1'
    ;(fake as { messages: { value: unknown[] } }).messages.value = []
  })

  it('stays on the new chat at the root path instead of reopening the last conversation', async () => {
    // The browser remembers a conversation from an earlier visit; the home
    // page is the new-conversation page now, so nothing navigates away from it
    // (plan 072).
    persistLastSessionId('s-last')
    sessionIdRef().value = null
    const router = makeRouter()
    const replace = vi.spyOn(router, 'replace')
    const wrapper = await mountView({}, router)
    await flushPromises()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="chat-greeting"]').exists()).toBe(true)
    // Nothing was open, so nothing is cleared.
    expect(resetSpy()).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('closes the workbench when the address bar loses its conversation', async () => {
    const workbench = useWorkbench()
    sessionIdRef().value = 's1'
    const router = makeRouter()
    const wrapper = await mountView({}, router, '/?session=s1')
    await flushPromises()
    const hasToggle = () => (useTopbarContent().value?.trailing?.() ?? [])
      .some((node) => node.props?.['data-testid'] === 'workbench-toggle')
    // The third column is open on a conversation…
    workbench.open.value = true
    expect(hasToggle()).toBe(true)
    // …and asking for a new chat leaves it behind with the conversation.
    await router.replace({ query: { session: undefined } })
    await flushPromises()
    expect(workbench.open.value).toBe(false)
    // The thread goes with it: the new-chat page is the page itself, so the
    // conversation that was on screen must not stay there (plan 072).
    expect(resetSpy()).toHaveBeenCalled()
    wrapper.unmount()
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

/**
 * Saving an attachment into the project (plan 030): a file added to the
 * composer — picked, dropped or pasted — pops the "save to" dialog at once,
 * the upload runs on confirm into the directory the user chose, and a confirm
 * leaves the file in the composer's attachment strip. The upload no longer
 * waits for the send.
 */
describe('ChatView saves attachments into the project', () => {
  beforeAll(() => {
    // The composer's own add path calls URL.createObjectURL on each attached
    // file; jsdom's shim cannot take a File made in a test.
    ;(URL as unknown as { createObjectURL: () => string }).createObjectURL = () => 'blob:mock'
    ;(URL as unknown as { revokeObjectURL: () => void }).revokeObjectURL = () => {}
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function pasteImage(wrapper: Awaited<ReturnType<typeof mountView>>, file: File) {
    await wrapper.find('textarea').trigger('paste', {
      clipboardData: { items: [{ kind: 'file', getAsFile: () => file }] },
    })
    await flushPromises()
  }

  it('pops the dialog on paste and uploads into the chosen directory on confirm', async () => {
    const upload = vi.spyOn(forebrainApi, 'filesUpload').mockResolvedValue({ fileId: 'f1' })
    vi.spyOn(forebrainApi, 'chatSession').mockResolvedValue({
      id: 's1', title: '', project: { id: 'p1', name: 'demo', root: '/p', lastAttachDir: 'docs' },
    })
    vi.spyOn(forebrainApi, 'projectDirs').mockResolvedValue({ path: '', entries: [] })

    const wrapper = await mountView({ teleport: true })
    await flushPromises()
    const file = new File(['x'], 'shot.png', { type: 'image/png' })
    await pasteImage(wrapper, file)

    // The dialog is up, nothing has uploaded yet.
    const confirm = wrapper.find('[data-testid="attach-confirm"]')
    expect(confirm.exists()).toBe(true)
    expect(upload).not.toHaveBeenCalled()

    await confirm.trigger('click')
    await flushPromises()
    // The upload ran into the project's last attach directory, and the file is
    // now in the composer's strip.
    expect(upload).toHaveBeenCalledWith(file, 's1', 'docs')
    expect(wrapper.text()).toContain('shot.png')
    expect(wrapper.find('[data-testid="attach-confirm"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('drops the file when the dialog is cancelled', async () => {
    const upload = vi.spyOn(forebrainApi, 'filesUpload').mockResolvedValue({ fileId: 'f1' })
    vi.spyOn(forebrainApi, 'chatSession').mockResolvedValue({
      id: 's1', title: '', project: { id: 'p1', name: 'demo', root: '/p', lastAttachDir: '' },
    })
    vi.spyOn(forebrainApi, 'projectDirs').mockResolvedValue({ path: '', entries: [] })

    const wrapper = await mountView({ teleport: true })
    await flushPromises()
    await pasteImage(wrapper, new File(['x'], 'shot.png', { type: 'image/png' }))
    await wrapper.find('[data-testid="attach-cancel"]').trigger('click')
    await flushPromises()

    expect(upload).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('shot.png')
    wrapper.unmount()
  })
})
