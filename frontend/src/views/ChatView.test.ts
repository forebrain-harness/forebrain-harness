import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { nextTick } from 'vue'

import { setLocale } from '@/locales'
import { forebrainApi } from '@/lib/api'

// The stream the component talks to, filled in by the module mock below and
// driven by the test. useChatStream is the only module replaced: the
// conversation's send path and every subagent-view call land on these spies.
const fake = vi.hoisted(() => ({} as Record<string, unknown>))

vi.mock('@/composables/useChatStream', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/composables/useChatStream')>()
  const { ref } = await import('vue')
  const f = fake as {
    sessionId: unknown
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
  }
  f.sessionId = ref('s1')
  f.subagents = ref([])
  f.subagentPendingInput = ref({})
  f.subagentReturnedDraft = ref({})
  f.withdrawSubagentInput = vi.fn()
  f.interruptSubagentToSend = vi.fn()
  f.sendToSubagent = vi.fn()
  f.recallSubagentInput = vi.fn()
  f.loadSubagentBudget = vi.fn()
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
      messages: ref([]),
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
      send: vi.fn(),
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

describe('ChatView subagent view', () => {
  beforeAll(() => {
    setLocale('en')
    // jsdom has no ResizeObserver or matchMedia; the thread and the workbench
    // breakpoint need both.
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
  afterEach(() => {
    vi.clearAllMocks()
    ;(fake as { subagents: { value: unknown } }).subagents.value = []
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
