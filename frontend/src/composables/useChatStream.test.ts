import { describe, expect, it, beforeAll, afterAll, vi } from 'vitest'

import { setLocale, t } from '@/locales'
import { forebrainApi } from '@/lib/api'
import {
  applyAssistantRunEventMetadata,
  applyCompactionUpdate,
  appendGoalLine,
  conversationFromTranscript,
  emptyContextRuntimeSignals,
  emptyPendingInputPreview,
  applyPlanUpdateToMessage,
  computeWorkedDurationMs,
  formatRuntimeStatusLabel,
  formatToolDuration,
  formatWorkedDurationLabel,
  subagentCallView,
  workedLineText,
  skillCardTitle,
  toolStepFromPayload,
  toolStepFromRow,
  type ChatMessage,
  type SubagentCall,
  type SubagentToolStep,
  type TimelineBlock,
  useChatStream,
} from './useChatStream'

class FakeChatWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static instances: FakeChatWebSocket[] = []

  readyState = FakeChatWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  sent: string[] = []

  constructor(readonly url: string) {
    FakeChatWebSocket.instances.push(this)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close() {
    this.readyState = FakeChatWebSocket.CLOSED
    this.onclose?.()
  }

  open() {
    this.readyState = FakeChatWebSocket.OPEN
    this.onopen?.()
  }

  message(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) })
  }
}

describe('useChatStream helpers', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))
  it('draws one card per compaction and moves it through its lifecycle', () => {
    let blocks: TimelineBlock[] = [{ kind: 'assistant', text: 'working on it', open: true }]
    blocks = applyCompactionUpdate(blocks, { compactionId: 'c1', patch: { status: 'running', percent: 0, tokensBefore: 182400 } })
    // The card closes the text streamed before it, as a tool call does.
    expect(blocks[0]).toMatchObject({ kind: 'assistant', open: false })
    expect(blocks[1]).toMatchObject({ kind: 'compaction', id: 'compaction:c1', compaction: { status: 'running', percent: 0 } })
    blocks = applyCompactionUpdate(blocks, { compactionId: 'c1', patch: { status: 'running', percent: 40, phase: 'summarizing' } })
    // A late, lower report never moves the bar backwards.
    blocks = applyCompactionUpdate(blocks, { compactionId: 'c1', patch: { status: 'running', percent: 12, phase: 'summarizing' } })
    expect(blocks).toHaveLength(2)
    expect(blocks[1]).toMatchObject({ compaction: { percent: 40, phase: 'summarizing', tokensBefore: 182400 } })
    blocks = applyCompactionUpdate(blocks, { compactionId: 'c1', patch: { status: 'done', percent: 100, tokensAfter: 12300, summary: 'checkpoint' } })
    expect(blocks[1]).toMatchObject({ compaction: { status: 'done', percent: 100, tokensBefore: 182400, tokensAfter: 12300, summary: 'checkpoint' } })
  })

  it('places a history compaction row in the turn it belongs to', () => {
    const rows = [
      { role: 'user', content: 'first question' },
      { role: 'assistant', content: 'first answer' },
      { role: 'compaction', content: '', compaction: { status: 'done', compactionId: 'c1', trigger: 'manual', tokensBefore: 900, tokensAfter: 100 } },
      { role: 'user', content: 'second question' },
      { role: 'compaction', content: '', compaction: { status: 'failed', compactionId: 'c2', trigger: 'auto', error: 'provider down' } },
      { role: 'assistant', content: 'second answer' },
    ]
    const conversation = conversationFromTranscript(rows)
    expect(conversation.map((m) => m.role)).toEqual(['user', 'assistant', 'assistant', 'user', 'assistant'])
    // A manual compaction between turns is a card of its own.
    expect(conversation[2].blocks).toEqual([
      { kind: 'compaction', id: 'compaction:c1', compaction: expect.objectContaining({ status: 'done', percent: 100 }) },
    ])
    // A pre-turn one opens the answer to the message it made room for.
    expect(conversation[4].blocks?.[0]).toMatchObject({ kind: 'compaction', compaction: { status: 'failed', error: 'provider down' } })
    expect(conversation[4].blocks?.[1]).toMatchObject({ kind: 'assistant', text: 'second answer' })
  })

  it('closes each run with its worked row, a run that said nothing included', () => {
    const rows = [
      { role: 'user', content: 'first', runId: 'r1' },
      { role: 'assistant', content: 'answer', runId: 'r1' },
      { role: 'worked', content: '', runId: 'r1', runStartedAt: '2026-10-03T09:00:00Z', runFinishedAt: '2026-10-03T09:00:05Z', workedDurationMs: 5000, planDone: 1, planTotal: 2, planActive: 'tests' },
      // A run that failed before it said anything: its message, then its line.
      { role: 'user', content: 'second', runId: 'r2' },
      { role: 'worked', content: '', runId: 'r2', runStartedAt: '2026-10-03T09:01:00Z', runFinishedAt: '2026-10-03T09:01:02Z', workedDurationMs: 2000 },
      { role: 'user', content: 'third' },
    ]
    const conversation = conversationFromTranscript(rows)
    expect(conversation.map((m) => `${m.role}:${m.content}`)).toEqual(['user:first', 'assistant:answer', 'user:second', 'assistant:', 'user:third'])
    expect(conversation[1]).toMatchObject({ runId: 'r1', workedDurationMs: 5000, workedPlanDone: 1, workedPlanTotal: 2, workedPlanActive: 'tests', runFinishedAt: '2026-10-03T09:00:05Z' })
    expect(conversation[3]).toMatchObject({ runId: 'r2', workedDurationMs: 2000 })
    expect(conversation[3].blocks).toBeUndefined()
  })

  it('closes what a round streamed before the goal line that follows it', () => {
    let blocks: TimelineBlock[] = [{ kind: 'assistant', text: 'round one', open: true }]
    blocks = appendGoalLine(blocks, { phase: 'round', round: 2, why: 'two tests fail', checkAgentId: 'check-1' })
    expect(blocks[0]).toMatchObject({ kind: 'assistant', text: 'round one', open: false })
    expect(blocks[1]).toEqual({ kind: 'goal', id: 'goal:round:2', goal: { phase: 'round', round: 2, why: 'two tests fail', checkAgentId: 'check-1' } })
  })

  it('places a history goal row in the turn working toward the goal', () => {
    const rows = [
      { role: 'user', content: '/goal ship' },
      { role: 'goal', content: '', goal: { phase: 'started', objective: 'ship' } },
      { role: 'assistant', content: 'round one' },
      { role: 'goal', content: '', goal: { phase: 'round', round: 2, why: 'two tests fail', checkAgentId: 'check-1' } },
      { role: 'assistant', content: 'round two' },
      { role: 'goal', content: '', goal: { phase: 'completed', status: 'done', rounds: 2, why: 'all pass', durationMs: 8000, checkAgentId: 'check-2' } },
      { role: 'user', content: 'thanks' },
    ]
    const conversation = conversationFromTranscript(rows)
    expect(conversation.map((m) => m.role)).toEqual(['user', 'assistant', 'user'])
    expect(conversation[1].blocks?.map((block) => block.kind === 'goal' ? `goal:${block.goal.phase}` : `${block.kind}:${'text' in block ? block.text : ''}`)).toEqual([
      'goal:started', 'assistant:round one', 'goal:round', 'assistant:round two', 'goal:completed',
    ])
    expect(conversation[1].blocks?.[4]).toMatchObject({ kind: 'goal', goal: { status: 'done', rounds: 2, durationMs: 8000, checkAgentId: 'check-2' } })
  })

  it('names the goal round on the working line', () => {
    const working = { kind: 'working' as const, startedAt: 0, elapsedMs: 5000 }
    expect(formatRuntimeStatusLabel({ ...working, goalRound: 2 }).label).toContain('round 2')
    expect(formatRuntimeStatusLabel({ ...working, goalRound: 2, goalChecking: true }).label).toContain('after round 2')
  })

  it('applies token budget metadata for assistant messages', () => {
    const compacted: ChatMessage = {
      id: 'a1',
      role: 'assistant',
      content: 'done',
    }

    const budgeted = applyAssistantRunEventMetadata(compacted, {
      type: 'token_budget_updated',
      payload: {
        model: 'gpt-5',
        token_usage: 4096,
        percent_left: 98,
        context_window: 200000,
        effective_window: 180000,
        auto_compact_threshold: 167000,
      },
    })

    expect(budgeted.tokenBudget).toMatchObject({
      model: 'gpt-5',
      tokenUsage: 4096,
      percentLeft: 98,
      contextWindow: 200000,
      effectiveWindow: 180000,
      autoCompactThreshold: 167000,
    })
  })

  it('applies updated plan payloads to assistant messages', () => {
    const base: ChatMessage = {
      id: 'a1',
      role: 'assistant',
      content: '',
    }

    const direct = applyPlanUpdateToMessage(base, {
      title: 'Updated Plan',
      explanation: 'Writing tests',
      completed: 1,
      total: 2,
      items: [{ content: 'Write tests', status: 'in_progress', active: 'Writing tests' }],
    })

    expect(direct.planUpdates).toEqual([
      {
        title: 'Updated Plan',
        explanation: 'Writing tests',
        completed: 1,
        total: 2,
        items: [{ content: 'Write tests', status: 'in_progress', active: 'Writing tests' }],
      },
    ])

    const fromEvent = applyAssistantRunEventMetadata(base, {
      type: 'plan_updated',
      payload: {
        title: 'Updated Plan',
        explanation: 'Shipping',
        completed: 1,
        total: 3,
        items: [{ id: '1', content: 'Ship', status: 'pending', active: 'Shipping' }],
      },
    })

    expect(fromEvent.planUpdates?.[0]).toEqual({
      title: 'Updated Plan',
      explanation: 'Shipping',
      completed: 1,
      total: 3,
      items: [{ id: '1', content: 'Ship', status: 'pending', active: 'Shipping' }],
    })
  })

  it('computes worked duration from run event timestamps', () => {
    expect(computeWorkedDurationMs('2026-05-11T10:00:00.000Z', '2026-05-11T10:09:09.000Z')).toBe(549000)
    expect(computeWorkedDurationMs('2026-05-11T10:00:00.000Z', '2026-05-11T09:59:59.000Z')).toBeUndefined()
    expect(computeWorkedDurationMs(undefined, '2026-05-11T10:09:09.000Z')).toBeUndefined()
  })

  it('closes every run with its worked line, however short', () => {
    expect(workedLineText(formatWorkedDurationLabel(549000))).toBe('Worked for 9m 09s')
    expect(workedLineText(formatWorkedDurationLabel(42000))).toBe('Worked for 42s')
    expect(workedLineText(formatWorkedDurationLabel(300))).toBe('Worked for 1s')
    expect(workedLineText(formatWorkedDurationLabel(0))).toBe('Worked for 0s')
    expect(workedLineText(formatWorkedDurationLabel(61999))).toBe('Worked for 1m 01s')
    expect(workedLineText(formatWorkedDurationLabel(3723000))).toBe('Worked for 1h 02m 03s')
    expect(workedLineText(formatWorkedDurationLabel(undefined))).toBe('')
    const finished = new Date(2026, 8, 25, 15, 4, 0)
    expect(workedLineText(formatWorkedDurationLabel(1200, undefined, finished.toISOString()))).toBe('Worked for 1s · 15:04')
  })

  it('carries the checklist progress the engine reported, and drops empty checklists', () => {
    const finished = new Date(2026, 8, 25, 15, 4, 0)
    const withPlan = formatWorkedDurationLabel(72_000, undefined, finished.toISOString(), { done: 3, total: 5, active: 'short task' })
    expect(withPlan).toEqual({ label: 'Worked for 1m 12s', plan: { done: 3, total: 5, active: 'short task' }, time: '15:04' })
    expect(workedLineText(withPlan)).toBe('Worked for 1m 12s · 3/5 · short task · 15:04')
    // A checklist of zero total is no checklist at all.
    expect(formatWorkedDurationLabel(1000, undefined, undefined, { done: 0, total: 0 }).plan).toBeUndefined()
  })

  it('formats active runtime labels with interrupt hints', () => {
    expect(formatRuntimeStatusLabel({ kind: 'working', elapsedMs: 141000 }).label).toBe('Working (2m 21s • esc to interrupt)')
    expect(formatRuntimeStatusLabel({ kind: 'working', elapsedMs: 141000, toolName: 'shell' }).label).toBe('Waiting for background terminal (2m 21s • esc to interrupt)')
    // The checklist is structure the caller draws, never English baked into
    // the sentence.
    expect(formatRuntimeStatusLabel({
      kind: 'working',
      elapsedMs: 141000,
      planCompleted: 1,
      planTotal: 3,
      planActive: 'Writing tests',
    })).toEqual({ label: 'Working (2m 21s • esc to interrupt)', plan: { done: 1, total: 3, active: 'Writing tests' } })
    expect(formatRuntimeStatusLabel({ kind: 'reconnecting', elapsedMs: 67000, attempt: 1, maxAttempts: 5 }).label).toBe('Reconnecting... 1/5 (1m 07s • esc to interrupt)')
    expect(formatRuntimeStatusLabel({ kind: 'idle', elapsedMs: 0 })).toEqual({ label: '' })
  })

  it('bumps context runtime signals on compact and budget events', () => {
    const stream = useChatStream()
    expect(stream.contextSignals.value.compactVersion).toBe(0)
    expect(stream.contextSignals.value.budgetVersion).toBe(0)
  })

  it('resets local session state through the shared session switch path', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValueOnce([
      { id: 'new-user', role: 'user', content: 'fresh question' },
    ])
    const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValueOnce({
      mode: 'agent',
      phase: '',
    })
    try {
      const stream = useChatStream()
      stream.switchToSession('old-session')
      await Promise.resolve()

      stream.messages.value = [
        { id: 'old-user', role: 'user', content: 'old question' },
        { id: 'old-assistant', role: 'assistant', content: 'old answer' },
      ]
      // A send is in flight with no run yet, and a message waits behind it.
      stream.isStreaming.value = true
      const held = stream.send('old queued')
      expect(stream.pendingInputPreview.value.queuedMessages).toEqual(['old queued'])
      stream.contextSignals.value = { compactVersion: 2, budgetVersion: 3, activeRunId: 'old-run' }
      stream.answer.value = 'partial old answer'
      stream.error.value = 'old error'

      stream.switchToSession('new-session')
      expect(stream.sessionId.value).toBe('new-session')
      // It belongs to no conversation now, so it goes back to the composer.
      await held
      expect(stream.takeReturnedDraft()).toEqual({ text: 'old queued', attachments: [], mentionImages: [] })
      expect(stream.messages.value).toEqual([])
      expect(stream.pendingInputPreview.value).toEqual(emptyPendingInputPreview())
      expect(stream.contextSignals.value).toEqual(emptyContextRuntimeSignals())
      expect(stream.answer.value).toBe('')
      expect(stream.error.value).toBeNull()

      await Promise.resolve()
      expect(chatMessagesSpy).toHaveBeenLastCalledWith('new-session', 0)
      expect(sessionModeSpy).toHaveBeenLastCalledWith('new-session')
    } finally {
      chatMessagesSpy.mockRestore()
      sessionModeSpy.mockRestore()
    }
  })

  it('resets and completes a websocket send when slash switches session', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([])
    const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({
      mode: 'agent',
      phase: '',
    })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'old-session'
      stream.messages.value = [
        { id: 'old-user', role: 'user', content: 'old question' },
        { id: 'old-assistant', role: 'assistant', content: 'old answer' },
      ]
      stream.contextSignals.value = { compactVersion: 4, budgetVersion: 5, activeRunId: 'old-run' }

      const pendingSend = stream.send('/new')
      const socket = FakeChatWebSocket.instances[0]
      expect(socket).toBeTruthy()
      socket.open()
      // Sent while /new runs: it waits behind the command, not beside it.
      const held = stream.send('old queued')
      expect(FakeChatWebSocket.instances).toHaveLength(1)
      expect(stream.pendingInputPreview.value.queuedMessages).toEqual(['old queued'])
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      expect(sent).toMatchObject({
        op: 'start_run',
        session_id: 'old-session',
      })
      expect((sent.message as Record<string, unknown>).content).toBe('/new')

      socket.message({
        op: 'session_bound',
        request_id: sent.request_id,
        session_id: 'new-session',
        message: 'session: switched',
        data: { session_switched: true },
      })
      await pendingSend
      await held
      expect(stream.takeReturnedDraft()).toEqual({ text: 'old queued', attachments: [], mentionImages: [] })

      expect(stream.sessionId.value).toBe('new-session')
      expect(stream.messages.value).toEqual([])
      expect(stream.pendingInputPreview.value).toEqual(emptyPendingInputPreview())
      expect(stream.contextSignals.value).toEqual(emptyContextRuntimeSignals())
      expect(stream.answer.value).toBe('')
      expect(stream.isStreaming.value).toBe(false)
      expect(stream.runtimeStatus.value.kind).toBe('idle')
      expect(chatMessagesSpy).toHaveBeenLastCalledWith('new-session', 0)
      expect(sessionModeSpy).toHaveBeenLastCalledWith('new-session')
    } finally {
      chatMessagesSpy.mockRestore()
      sessionModeSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('renders a slash reply as a system notice without a run lifecycle', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([])
    const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({
      mode: 'agent',
      phase: '',
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('/status')
      const socket = FakeChatWebSocket.instances[0]
      expect(socket).toBeTruthy()
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>

      socket.message({
        op: 'session_bound',
        request_id: sent.request_id,
        session_id: 'web-slash',
      })
      socket.message({
        op: 'slash_reply',
        request_id: sent.request_id,
        session_id: 'web-slash',
        text: 'Session Status',
      })
      await pendingSend

      expect(stream.messages.value).toHaveLength(2)
      expect(stream.messages.value[0]).toMatchObject({ role: 'user', content: '/status' })
      expect(stream.messages.value[1]).toMatchObject({ role: 'notice', content: 'Session Status' })
      expect(stream.isStreaming.value).toBe(false)
      expect(socket.readyState).toBe(FakeChatWebSocket.CLOSED)
    } finally {
      chatMessagesSpy.mockRestore()
      sessionModeSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  // The guard against a stale snapshot is about sends that came after the
  // snapshot was requested. A send that finished before it is already in the
  // history the snapshot reads, so returning to that session must load it.
  it('loads the history of a session the last send went into', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'user-1', role: 'user', content: '/status' },
      { id: 'assistant-1', role: 'assistant', content: 'persisted answer' },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'sent-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'sent-session', records: [] })
    const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('/status', { sessionId: 'sent-session' })
      const socket = FakeChatWebSocket.instances[0]
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      socket.message({ op: 'session_bound', request_id: sent.request_id, session_id: 'sent-session' })
      socket.message({ op: 'slash_reply', request_id: sent.request_id, session_id: 'sent-session', text: 'Session Status' })
      await pendingSend

      // Leave and come back: the switch clears the view, and the snapshot
      // must fill it again.
      stream.switchToSession('other-session')
      stream.sessionId.value = 'sent-session'
      stream.messages.value = []
      await stream.loadMessages('sent-session')

      expect(stream.messages.value.map((message) => message.content)).toContain('persisted answer')
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
      sessionModeSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
  })

  it('shows the picker a slash command offers and sends a pick back without a user bubble', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([])
    const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('/model')
      const socket = FakeChatWebSocket.instances[0]!
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      socket.message({ op: 'session_bound', request_id: sent.request_id, session_id: 'web-pick' })
      socket.message({
        op: 'slash_reply',
        request_id: sent.request_id,
        session_id: 'web-pick',
        text: '',
        data: { picker: { command: 'model', title: 'Model', hint: 'The model this agent runs on', items: [
          { value: 'openai / gpt-4o', label: 'openai / gpt-4o', current: true },
          { value: 'deepseek / deepseek-chat', label: 'deepseek / deepseek-chat' },
        ] } },
      })
      await pendingSend
      const notice = stream.messages.value[1]!
      expect(notice).toMatchObject({ role: 'notice', picker: { command: 'model', title: 'Model' } })
      expect(notice.picker?.items[0]?.current).toBe(true)

      const socketsBefore = FakeChatWebSocket.instances.length
      const pendingPick = stream.choose(notice.id!, { command: 'model', value: 'deepseek / deepseek-chat' })
      for (const candidate of FakeChatWebSocket.instances.slice(socketsBefore)) candidate.open()
      const pickSocket = FakeChatWebSocket.instances.slice(socketsBefore).find((candidate) =>
        candidate.sent.some((raw) => JSON.parse(raw).op === 'start_run'))!
      const pick = JSON.parse(pickSocket.sent.find((raw) => JSON.parse(raw).op === 'start_run')!) as { request_id?: string; message?: { content?: string; choice?: unknown } }
      expect(pick.message).toMatchObject({ content: '/model', choice: { command: 'model', value: 'deepseek / deepseek-chat' } })
      pickSocket.message({ op: 'slash_reply', request_id: pick.request_id, session_id: 'web-pick', text: 'Switched to deepseek / deepseek-chat.' })
      await pendingPick

      expect(stream.messages.value.map((m) => m.role)).toEqual(['user', 'notice', 'notice'])
      expect(stream.messages.value[1]).toMatchObject({ picked: 'deepseek / deepseek-chat' })
      expect(stream.messages.value[2]).toMatchObject({ role: 'notice', content: 'Switched to deepseek / deepseek-chat.' })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionModeSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('withdraws a message when the user stops the compaction it waits on', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([])
    const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({
      mode: 'agent',
      phase: '',
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('second question that needs room')
      const socket = FakeChatWebSocket.instances[0]
      expect(socket).toBeTruthy()
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      socket.message({ op: 'session_bound', request_id: sent.request_id, session_id: 'web-preturn' })
      const compactionEvent = (id: string, sequence: number, type: string, payload: Record<string, unknown>) => socket.message({
        op: 'run_event',
        data: { id, sequence, type, run_id: '', session_id: 'web-preturn', created_at: `2026-09-24T00:00:0${sequence}.000Z`, payload },
      })
      // The pre-turn compaction runs before any run exists.
      compactionEvent('evt-1', 1, 'context_compacting', { compaction_id: 'c-pre', trigger: 'auto', tokens_before: 1506 })
      compactionEvent('evt-2', 2, 'context_compact_progress', { compaction_id: 'c-pre', percent: 13, phase: 'reading' })

      stream.cancel()

      // The stop goes to the send on its own socket, which stays open to
      // report how the compaction ended.
      expect(JSON.parse(socket.sent[socket.sent.length - 1] ?? '{}')).toMatchObject({ op: 'cancel_command' })
      expect(socket.readyState).toBe(FakeChatWebSocket.OPEN)
      compactionEvent('evt-3', 3, 'context_compact_failed', { compaction_id: 'c-pre', trigger: 'auto', cancelled: true })
      socket.message({ op: 'turn_withdrawn', request_id: sent.request_id, session_id: 'web-preturn' })

      await pendingSend
      expect(stream.takeReturnedDraft()).toEqual({ text: 'second question that needs room', attachments: [], mentionImages: [] })
      // The message never became a turn; the compaction's card says it stopped.
      expect(stream.messages.value.some((message) => message.role === 'user')).toBe(false)
      expect(stream.messages.value).toHaveLength(1)
      expect(stream.messages.value[0].blocks).toEqual([
        expect.objectContaining({ kind: 'compaction', compaction: expect.objectContaining({ status: 'cancelled' }) }),
      ])
      expect(stream.error.value).toBeNull()
      expect(stream.isStreaming.value).toBe(false)
      expect(socket.readyState).toBe(FakeChatWebSocket.CLOSED)
    } finally {
      chatMessagesSpy.mockRestore()
      sessionModeSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('shows what a message attached, and gives it back whole with the reason when it cannot be sent', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    try {
      const stream = useChatStream()
      const attached = {
        text: '',
        attachments: [{ fileId: 'file-1', filename: 'report.pdf', mediaType: 'application/pdf' }],
        mentionImages: ['shots/diagram.png'],
      }
      const pendingSend = stream.send('', { attached })
      const socket = FakeChatWebSocket.instances[0]!
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as { request_id: string; message: Record<string, unknown> }
      expect(sent.message).toMatchObject({ content: '', attachments: ['file-1'], mention_images: ['shots/diagram.png'] })
      // The bubble names each thing, and says what the stored row will say.
      const bubble = stream.messages.value.find((message) => message.role === 'user')
      expect(bubble?.attachments).toEqual([
        { fileId: 'file-1', name: 'report.pdf', mediaType: 'application/pdf' },
        { path: 'shots/diagram.png', name: 'diagram.png' },
      ])
      expect(bubble?.content).toBe('[Attachment report.pdf]\n[Image #1]')

      socket.message({ op: 'turn_withdrawn', request_id: sent.request_id, session_id: 's-attach', error: 'ImageFile: read "shots/diagram.png": no such file or directory' })
      await pendingSend
      expect(stream.takeReturnedDraft()).toEqual(attached)
      expect(stream.takeReturnedNotice()).toBe('ImageFile: read "shots/diagram.png": no such file or directory')
      expect(stream.takeReturnedNoticeCode()).toBeNull()
      expect(stream.messages.value.some((message) => message.role === 'user')).toBe(false)
      expect(stream.isStreaming.value).toBe(false)
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
  })

  it('hands a withdrawn message back with the code behind the refusal sentence', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, value: FakeChatWebSocket })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('another turn is busy in here')
      const socket = FakeChatWebSocket.instances[0]!
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as { request_id: string }
      // The session already has one live run, so the runtime refuses to start
      // this one: the message never became a turn and comes back whole, with
      // both the sentence the runtime rendered and the stable code behind it.
      socket.message({
        op: 'turn_withdrawn',
        request_id: sent.request_id,
        session_id: 's-busy',
        error: 'This conversation is already running a turn; send again when it finishes.',
        data: { code: 'session_running' },
      })
      await pendingSend
      expect(stream.takeReturnedDraft()).toEqual({ text: 'another turn is busy in here', attachments: [], mentionImages: [] })
      expect(stream.takeReturnedNotice()).toBe('This conversation is already running a turn; send again when it finishes.')
      expect(stream.takeReturnedNoticeCode()).toBe('session_running')
      expect(stream.messages.value.some((message) => message.role === 'user')).toBe(false)
      expect(stream.isStreaming.value).toBe(false)
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, value: originalWebSocket })
    }
  })

  it('names what a stored message attached', () => {
    const conversation = conversationFromTranscript([
      { role: 'user', content: 'look at these', attachments: [
        { fileId: 'file-1', name: 'report.pdf', mediaType: 'application/pdf' },
        { path: 'shots/diagram.png', name: 'diagram.png', mediaType: 'image/png' },
      ] },
    ])
    expect(conversation[0].attachments).toEqual([
      { fileId: 'file-1', name: 'report.pdf', mediaType: 'application/pdf' },
      { path: 'shots/diagram.png', name: 'diagram.png', mediaType: 'image/png' },
    ])
  })

  it('preserves the current turn when websocket binds the first session', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([])
    const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({
      mode: 'agent',
      phase: '',
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('hello')
      const socket = FakeChatWebSocket.instances[0]
      expect(socket).toBeTruthy()
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>

      socket.message({
        op: 'session_bound',
        request_id: sent.request_id,
        session_id: 'web-first',
      })
      socket.message({
        op: 'run_started',
        request_id: sent.request_id,
        run_id: 'run-1',
        session_id: 'web-first',
      })
      socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: {
          id: 'evt-run-1-said',
          sequence: 2,
          type: 'assistant_delta',
          run_id: 'run-1',
          session_id: 'web-first',
          created_at: '2026-06-11T00:00:00.500Z',
          payload: { text: 'hi' },
        },
      })
      // The current gateway writes a durable canonical mirror immediately
      // before the legacy completion carrying the request-stream semantics.
      // Initial live connections must wait for that legacy frame; only a
      // resume_connection terminates from the canonical tail itself.
      socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: {
          id: 'evt-run-1-completed',
          sequence: 3,
          type: 'turn_completed',
          run_id: 'run-1',
          session_id: 'web-first',
          created_at: '2026-06-11T00:00:01.000Z',
          payload: { text: 'hi' },
        },
      })
      expect(socket.readyState).toBe(FakeChatWebSocket.OPEN)
      socket.message({
        op: 'run_completed',
        request_id: sent.request_id,
        run_id: 'run-1',
        session_id: 'web-first',
        text: 'hi',
        finished_at: '2026-06-11T00:00:01.000Z',
      })
      await pendingSend

      expect(stream.sessionId.value).toBe('web-first')
      expect(stream.messages.value).toHaveLength(2)
      expect(stream.messages.value[0]).toMatchObject({ role: 'user', content: 'hello' })
      expect(stream.messages.value[1]).toMatchObject({ role: 'assistant', content: 'hi' })
      expect(chatMessagesSpy).not.toHaveBeenCalled()
      expect(sessionModeSpy).not.toHaveBeenCalled()
    } finally {
      chatMessagesSpy.mockRestore()
      sessionModeSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  // The worked line names the checklist however a run ends, live as on a
  // reload: the request socket's own ending carries the facts.
  for (const ending of ['run_completed', 'run_cancelled', 'run_error'] as const) {
    it(`a live ${ending} closes the turn with its checklist facts`, async () => {
      FakeChatWebSocket.instances = []
      const originalWebSocket = globalThis.WebSocket
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
      const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([])
      const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' })
      try {
        const stream = useChatStream()
        const pendingSend = stream.send('hello')
        const socket = FakeChatWebSocket.instances[0]
        socket.open()
        const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
        socket.message({ op: 'session_bound', request_id: sent.request_id, session_id: 'web-plan' })
        socket.message({ op: 'run_started', request_id: sent.request_id, run_id: 'run-plan', session_id: 'web-plan' })
        socket.message({
          op: 'run_event',
          request_id: sent.request_id,
          data: {
            id: 'evt-plan-said', sequence: 2, type: 'assistant_delta', run_id: 'run-plan', session_id: 'web-plan',
            created_at: '2026-06-11T00:00:00.500Z', payload: { text: 'working' },
          },
        })
        socket.message({
          op: ending,
          request_id: sent.request_id,
          run_id: 'run-plan',
          session_id: 'web-plan',
          text: 'done',
          error: ending === 'run_error' ? 'boom' : undefined,
          finished_at: '2026-06-11T00:00:01.000Z',
          plan_done: 2,
          plan_total: 5,
          plan_active: 'wire the API',
        })
        await pendingSend.catch(() => {})

        expect(stream.messages.value[1]).toMatchObject({
          role: 'assistant',
          workedPlanDone: 2,
          workedPlanTotal: 5,
          workedPlanActive: 'wire the API',
        })
      } finally {
        chatMessagesSpy.mockRestore()
        sessionModeSpy.mockRestore()
        Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
      }
    })
  }

  // A run that said nothing still ran: however it ended, its turn stays to
  // carry the worked line that closes it, with no placeholder text.
  for (const ending of ['run_completed', 'run_cancelled', 'run_error'] as const) {
    it(`a live ${ending} with no output keeps its worked line`, async () => {
      FakeChatWebSocket.instances = []
      const originalWebSocket = globalThis.WebSocket
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
      const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([])
      const sessionModeSpy = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' })
      try {
        const stream = useChatStream()
        const pendingSend = stream.send('hello')
        const socket = FakeChatWebSocket.instances[0]
        socket.open()
        const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
        socket.message({ op: 'session_bound', request_id: sent.request_id, session_id: 'web-silent' })
        socket.message({ op: 'run_started', request_id: sent.request_id, run_id: 'run-silent', session_id: 'web-silent', started_at: '2026-06-11T00:00:00.000Z' })
        socket.message({
          op: ending,
          request_id: sent.request_id,
          run_id: 'run-silent',
          session_id: 'web-silent',
          error: ending === 'run_error' ? 'the provider refused the request' : undefined,
          finished_at: '2026-06-11T00:00:01.000Z',
          data: { elapsed_ms: 1200 },
        })
        await pendingSend.catch(() => {})

        const turn = stream.messages.value[1]
        expect(turn).toMatchObject({ role: 'assistant', workedDurationMs: expect.any(Number) })
        expect(String(turn?.content ?? '').trim()).not.toBe('...')
        if (ending === 'run_error') {
          // The failure is the turn's last word, above its worked line — not
          // a page-level alert detached from the run that failed.
          expect(turn?.blocks).toEqual([{ kind: 'error', id: 'error:run-silent', text: 'the provider refused the request' }])
          expect(stream.error.value).toBeNull()
          expect(turn?.content).toBe('')
        }
      } finally {
        chatMessagesSpy.mockRestore()
        sessionModeSpy.mockRestore()
        Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
      }
    })
  }

  it('reloads a failed run with its error inside the turn, not as a fresh alert', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'u1', role: 'user', content: 'hello', runId: 'r-failed' },
      { id: 'worked-r-failed', role: 'worked', content: '', runId: 'r-failed', runStartedAt: '2026-10-03T09:00:00Z', runFinishedAt: '2026-10-03T09:00:02Z', workedDurationMs: 2000 },
    ])
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 's-failed', nextCursor: 2, highWater: 2, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'e1', sequence: 1, schemaVersion: 1, sessionId: 's-failed', runId: 'r-failed', type: 'turn_started', createdAt: '2026-10-03T09:00:00Z', payload: {} },
        { id: 'e2', sequence: 2, schemaVersion: 1, sessionId: 's-failed', runId: 'r-failed', type: 'turn_error', createdAt: '2026-10-03T09:00:02Z', payload: { error: 'the provider refused the request' } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's-failed', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 's-failed'
      await stream.loadMessages('s-failed')

      expect(stream.messages.value.map((m) => m.role)).toEqual(['user', 'assistant'])
      expect(stream.messages.value[1]).toMatchObject({ runId: 'r-failed', workedDurationMs: expect.any(Number) })
      expect(stream.messages.value[1]?.blocks).toEqual([{ kind: 'error', id: 'error:r-failed', text: 'the provider refused the request' }])
      expect(stream.error.value).toBeNull()
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('reloads a steer the run was handed once, from the transcript', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'u1', role: 'user', content: 'run the tests', runId: 'r-steer' },
      { id: 'a1', role: 'assistant', content: 'checking the build', runId: 'r-steer' },
      { id: 'u2', role: 'user', content: 'also run lint', runId: 'r-steer' },
      { id: 'a2', role: 'assistant', content: 'lint is clean', runId: 'r-steer' },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 's-steer', nextCursor: 1, highWater: 1, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'e1', sequence: 1, schemaVersion: 1, sessionId: 's-steer', runId: 'r-steer', type: 'input_delivered', createdAt: '2026-10-03T09:00:01Z', payload: { text: 'also run lint' } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's-steer', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 's-steer'
      await stream.loadMessages('s-steer')

      expect(stream.messages.value.map((m) => m.role)).toEqual(['user', 'assistant', 'user', 'assistant'])
      expect(stream.messages.value.filter((m) => m.role === 'user' && m.content === 'also run lint')).toHaveLength(1)
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('keeps the projected prefix and resumes from its durable cursor after disconnect', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('stream through a reconnect')
      const first = FakeChatWebSocket.instances[0]
      expect(first).toBeTruthy()
      first!.open()

      const runEvent = (socket: FakeChatWebSocket, id: string, sequence: number, runId: string, type: string, payload: Record<string, unknown>) => {
        socket.message({
          op: 'run_event',
          data: {
            id,
            sequence,
            type,
            run_id: runId,
            session_id: 'session-reconnect',
            created_at: `2026-06-14T00:00:0${sequence}.000Z`,
            payload,
          },
        })
      }
      runEvent(first!, 'evt-1', 1, 'run-current', 'turn_started', {})
      runEvent(first!, 'evt-2', 2, 'run-current', 'assistant_delta', { text: 'partial' })
      expect(stream.messages.value[stream.messages.value.length - 1]?.content).toBe('partial')

      first!.close()
      // The retry is found by what it says, not by where it lands in the list:
      // a session observer opens its own socket too, and which of the two is
      // created first is not part of the contract under test.
      let retry: FakeChatWebSocket | undefined
      await vi.waitFor(() => {
        for (const candidate of FakeChatWebSocket.instances) {
          if (candidate === first || candidate.readyState !== FakeChatWebSocket.CONNECTING) continue
          candidate.open()
          if (String(JSON.parse(candidate.sent[0] ?? '{}').op ?? '') === 'resume_connection') {
            retry = candidate
          }
        }
        expect(retry).toBeTruthy()
      }, { timeout: 2500 })
      const resume = JSON.parse(retry!.sent[0] ?? '{}') as Record<string, unknown>
      expect(resume).toMatchObject({
        op: 'resume_connection',
        run_id: 'run-current',
        cursor: 2,
      })
      expect(stream.messages.value[stream.messages.value.length - 1]?.content).toBe('partial')

      // The session tail can contain another run. It must neither overwrite
      // this request's partial answer nor terminate this request socket.
      runEvent(retry!, 'evt-3', 3, 'run-other', 'turn_completed', { text: 'other answer' })
      expect(retry!.readyState).toBe(FakeChatWebSocket.OPEN)
      expect(stream.messages.value[stream.messages.value.length - 1]?.content).toBe('partial')

      runEvent(retry!, 'evt-4', 4, 'run-current', 'assistant_delta', { text: ' response' })
      runEvent(retry!, 'evt-5', 5, 'run-current', 'turn_completed', { text: 'partial response' })
      await pendingSend
      expect(stream.messages.value.find((message) => message.runId === 'run-current')?.content).toBe('partial response')
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('renders progress only from plan_updated run events', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('implement a multi-step change')
      const socket = FakeChatWebSocket.instances[0]
      expect(socket).toBeTruthy()
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>

      socket.message({
        op: 'plan',
        request_id: sent.request_id,
        data: {
          plan_id: 'legacy-plan',
          question: 'implement a multi-step change',
          steps: [{ step_id: 'ignored', description: 'Ignored' }],
        },
      })
      const afterLegacyPlan = stream.messages.value[stream.messages.value.length - 1]
      expect(afterLegacyPlan?.plan).toBeUndefined()
      expect(afterLegacyPlan?.planBlocks).toBeUndefined()
      expect(afterLegacyPlan?.planUpdates).toBeUndefined()

      socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: {
          type: 'plan_updated',
          run_id: 'run-1',
          session_id: 's1',
          created_at: '2026-06-14T00:00:00.000Z',
          payload: {
            title: 'Updated Plan',
            items: [{ id: '1', content: 'Inspect code', status: 'in_progress', active: 'Inspecting code' }],
            completed: 0,
            total: 1,
            explanation: 'Inspecting code',
          },
        },
      })
      const afterPlanUpdated = stream.messages.value[stream.messages.value.length - 1]
      expect(afterPlanUpdated?.planUpdates?.[0].items[0].content).toBe('Inspect code')

      socket.message({
        op: 'run_completed',
        request_id: sent.request_id,
        text: 'done',
        data: { elapsed_ms: 61999 },
      })
      await pendingSend
      expect(stream.messages.value[stream.messages.value.length - 1]).toMatchObject({
        role: 'assistant',
        workedDurationMs: 61999,
      })
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('keeps a subagent conversation out of the primary answer and in its own view', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('find the callers')
      const socket = FakeChatWebSocket.instances[0]
      expect(socket).toBeTruthy()
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      const runEvent = (type: string, payload: Record<string, unknown>) => socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: { type, run_id: 'run-1', session_id: 's1', created_at: '2026-06-14T00:00:00.000Z', payload },
      })

      runEvent('subagent_spawned', {
        agent_id: 'task-explore-1',
        agent_type: 'explore',
        task_id: 'task-explore-1',
        title: 'Trace RenderFrame callers',
        task: 'find the callers',
        execution_id: 'exec-1',
      })
      runEvent('reasoning_delta', { agent_id: 'task-explore-1', text: 'weighing where to look' })
      runEvent('assistant_delta', { agent_id: 'task-explore-1', text: 'Let me grep first.' })
      runEvent('tool_call_started', {
        step_id: 'call-1',
        tool_name: 'shell',
        summary: 'running rg caller',
        tool_meta: { tool_name: 'shell', status: 'running', agent_id: 'task-explore-1' },
      })
      runEvent('tool_call_completed', {
        step_id: 'call-1',
        tool_name: 'shell',
        summary: 'ran rg caller · 3 matches',
        display_body: 'pkg/run/subagent.go:1',
        tool_meta: { tool_name: 'shell', status: 'completed', agent_id: 'task-explore-1' },
      })
      runEvent('assistant_delta', { agent_id: 'task-explore-1', text: 'Three callers, all in pkg/run.' })
      runEvent('usage_delta', { agent_id: 'task-explore-1', input_tokens: 1200, output_tokens: 300 })
      runEvent('subagent_ended', {
        agent_id: 'task-explore-1',
        agent_type: 'explore',
        task_id: 'task-explore-1',
        status: 'ok',
        execution_id: 'exec-1',
      })
      // The primary agent's own answer, which is the only text the
      // conversation may show.
      runEvent('assistant_delta', { text: 'I checked with a subagent.' })

      const primary = stream.messages.value[stream.messages.value.length - 1]
      expect(primary?.content).toBe('I checked with a subagent.')
      expect(primary?.content).not.toContain('Three callers')
      // The conversation still says a subagent ran — one card of its own
      // (this dispatch had no calling tool), its row opening the agent's view.
      const standalone = (primary?.blocks ?? []).find((b) => b.kind === 'subagent')
      expect(standalone).toMatchObject({ id: 'subagent-exec:exec-1' })
      const standaloneStep = (standalone as Extract<TimelineBlock, { kind: 'subagent' }>)?.step
      expect(standaloneStep?.subagentCall?.tasks[0]).toMatchObject({ title: 'Trace RenderFrame callers' })
      expect(stream.subagentCallTasks.get('subagent-exec:exec-1')?.get(0)).toMatchObject({
        agentId: 'task-explore-1',
        status: 'done',
      })

      const entry = stream.subagents.value.find((row) => row.agentId === 'task-explore-1')
      expect(entry).toBeTruthy()
      expect(entry?.status).toBe('done')
      expect(entry?.inputTokens).toBe(1200)
      expect(entry?.outputTokens).toBe(300)
      // Ordered exactly as the subagent produced it: the prompt it was given,
      // its thinking, the text before the call, the call, its conclusion, and
      // the worked line that closes the execution.
      expect(entry?.blocks.map((block) => block.kind)).toEqual([
        'prompt',
        'thinking',
        'assistant',
        'tool',
        'assistant',
        'worked',
      ])
      const [prompt, thinking, preamble, toolBlock, answer] = entry!.blocks
      expect(prompt).toMatchObject({ kind: 'prompt', text: 'find the callers' })
      expect(thinking).toMatchObject({ kind: 'thinking', text: 'weighing where to look' })
      expect(preamble).toMatchObject({ kind: 'assistant', text: 'Let me grep first.' })
      expect(toolBlock).toMatchObject({
        kind: 'tool',
        step: { toolName: 'shell', summary: 'ran rg caller · 3 matches', status: 'completed', output: 'pkg/run/subagent.go:1' },
      })
      expect(answer).toMatchObject({ kind: 'assistant', text: 'Three callers, all in pkg/run.' })

      // Repeating exactly the same continue instruction is still another
      // historical prompt, and a non-streaming continuation contributes its
      // own final assistant block instead of being hidden by the old answer.
      runEvent('subagent_spawned', {
        agent_id: 'task-explore-1', agent_type: 'explore', task_id: 'task-explore-1',
        task: 'find the callers', execution_id: 'exec-2',
      })
      runEvent('subagent_ended', {
        agent_id: 'task-explore-1', agent_type: 'explore', task_id: 'task-explore-1',
        status: 'ok', output: 'No additional callers.', execution_id: 'exec-2',
      })
      const continued = stream.subagents.value.find((row) => row.agentId === 'task-explore-1')
      expect(continued?.blocks.filter((block) => block.kind === 'prompt')).toHaveLength(2)
      expect(continued?.blocks[continued.blocks.length - 2]).toMatchObject({ kind: 'assistant', text: 'No additional callers.' })
      // The second execution closes with its own worked line.
      expect(continued?.blocks[continued.blocks.length - 1]?.kind).toBe('worked')

      socket.message({
        op: 'run_completed',
        request_id: sent.request_id,
        text: 'I checked with a subagent.',
        data: { elapsed_ms: 1200 },
      })
      await pendingSend
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('closes streamed text when thinking starts, as text closes thinking', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('check the ledger')
      const socket = FakeChatWebSocket.instances[0]
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      const runEvent = (type: string, payload: Record<string, unknown>) => socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: { type, run_id: 'run-1', session_id: 's1', created_at: '2026-06-14T00:00:00.000Z', payload },
      })

      runEvent('subagent_spawned', {
        agent_id: 'task-r', agent_type: 'explore', task_id: 'task-r',
        task: 'check the ledger', execution_id: 'exec-1',
      })
      runEvent('assistant_delta', { agent_id: 'task-r', text: 'step one.' })
      runEvent('reasoning_delta', { agent_id: 'task-r', text: 'weighing' })
      runEvent('assistant_delta', { agent_id: 'task-r', text: 'step two.' })

      const entry = stream.subagents.value.find((row) => row.agentId === 'task-r')
      const blocks = entry?.blocks.filter((block) => block.kind === 'assistant' || block.kind === 'thinking') ?? []
      expect(blocks.map((block) => block.kind)).toEqual(['assistant', 'thinking', 'assistant'])
      // A block of one kind ends when a block of the other starts, in either
      // direction: the text before the thinking is closed, so the text after
      // it is its own block rather than a repeat of the whole answer.
      expect(blocks[0]).toMatchObject({ kind: 'assistant', text: 'step one.', open: false })
      expect(blocks[2]).toMatchObject({ kind: 'assistant', text: 'step two.', open: true })

      socket.message({
        op: 'run_completed',
        request_id: sent.request_id,
        text: 'checked',
        data: { elapsed_ms: 1000 },
      })
      await pendingSend
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('closes each execution of one subagent with its own worked line', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('review twice')
      const socket = FakeChatWebSocket.instances[0]
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      const at = (seconds: number) => new Date(Date.parse('2026-06-14T00:00:00.000Z') + seconds * 1000).toISOString()
      const runEvent = (type: string, payload: Record<string, unknown>, seconds: number) => socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: { type, run_id: 'run-1', session_id: 's1', created_at: at(seconds), payload },
      })

      runEvent('subagent_spawned', {
        agent_id: 'task-1', agent_type: 'explore', task_id: 'task-1',
        task: 'first pass', execution_id: 'exec-1',
      }, 1)
      runEvent('subagent_ended', { agent_id: 'task-1', agent_type: 'explore', status: 'ok', execution_id: 'exec-1' }, 61)
      runEvent('subagent_spawned', {
        agent_id: 'task-1', agent_type: 'explore', task_id: 'task-1',
        task: 'continue', execution_id: 'exec-2',
      }, 300)
      runEvent('subagent_ended', { agent_id: 'task-1', agent_type: 'explore', status: 'ok', execution_id: 'exec-2' }, 330)
      // A redelivered end is one delivery too many, not a second execution.
      runEvent('subagent_ended', { agent_id: 'task-1', agent_type: 'explore', status: 'ok', execution_id: 'exec-2' }, 331)

      const entry = stream.subagents.value.find((row) => row.agentId === 'task-1')
      const worked = entry?.blocks.filter((block) => block.kind === 'worked') ?? []
      expect(worked).toHaveLength(2)
      expect(worked[0]).toMatchObject({ kind: 'worked', id: 'worked:exec-1', durationMs: 60_000 })
      expect(worked[1]).toMatchObject({ kind: 'worked', id: 'worked:exec-2', durationMs: 30_000 })

      socket.message({
        op: 'run_completed',
        request_id: sent.request_id,
        text: 'reviewed',
        data: { elapsed_ms: 1000 },
      })
      await pendingSend
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('reports the checklist on the working line as structure, from the payload’s own active task', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('fix the flow')
      const socket = FakeChatWebSocket.instances[0]
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      const runEvent = (type: string, payload: Record<string, unknown>) => socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: { type, run_id: 'run-1', session_id: 's1', created_at: '2026-06-14T00:00:00.000Z', payload },
      })

      // The conversation's plan: the working line keeps its facts, but the
      // task is the payload's own active title, not its explanation.
      runEvent('plan_updated', {
        title: 'Updated Plan', explanation: 'an explanation that is not the task',
        completed: 2, total: 5, active: 'Fix the reducer',
        items: [{ id: 'a', content: 'Fix the reducer', status: 'in_progress' }],
      })
      // A shell call starts the working line, the way every run does.
      runEvent('tool_call_started', { tool_name: 'shell' })
      const line = formatRuntimeStatusLabel(stream.runtimeStatus.value)
      expect(line.label).not.toContain('Tasks')
      expect(line.plan).toEqual({ done: 2, total: 5, active: 'Fix the reducer' })

      // A subagent's own plan lands in its own transcript, with the same
      // facts its worked line will close with.
      runEvent('subagent_spawned', {
        agent_id: 'task-2', agent_type: 'explore', task_id: 'task-2',
        task: 'read the source', execution_id: 'exec-9',
      })
      runEvent('plan_updated', {
        agent_id: 'task-2',
        title: 'Updated Plan', explanation: 'not the active title',
        completed: 1, total: 3, active: 'Reading the source',
        items: [{ id: 'b', content: 'Read the source', status: 'in_progress' }],
      })
      const entry = stream.subagents.value.find((row) => row.agentId === 'task-2')
      expect(entry?.plan).toEqual({ done: 1, total: 3, active: 'Reading the source' })

      socket.message({
        op: 'run_completed',
        request_id: sent.request_id,
        text: 'done',
        data: { elapsed_ms: 1000 },
      })
      await pendingSend
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  it('keeps a subagent todo list in that subagent view and off the conversation', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', {
      configurable: true,
      writable: true,
      value: FakeChatWebSocket,
    })
    try {
      const stream = useChatStream()
      const pendingSend = stream.send('migrate feedback')
      const socket = FakeChatWebSocket.instances[0]
      expect(socket).toBeTruthy()
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      const runEvent = (type: string, payload: Record<string, unknown>) => socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: { type, run_id: 'run-1', session_id: 's1', created_at: '2026-06-14T00:00:00.000Z', payload },
      })

      runEvent('subagent_spawned', {
        agent_id: 'task-explore-1', agent_type: 'explore', task_id: 'task-explore-1',
        task: 'read the source', execution_id: 'exec-1',
      })
      // The conversation's own plan. The active task is the payload's own
      // derivation, the way the engine publishes it.
      runEvent('plan_updated', {
        title: 'Updated Plan', explanation: 'Checking the target', completed: 1, total: 2,
        active: 'Checking the target',
        items: [{ id: 'a', content: 'Check the target', status: 'in_progress' }],
      })
      // The subagent's plan, which belongs to its own view.
      runEvent('plan_updated', {
        agent_id: 'task-explore-1',
        title: 'Updated Plan', explanation: 'Reading the source', completed: 0, total: 3,
        active: 'Reading the source',
        items: [{ id: 'b', content: 'Read the source', status: 'in_progress' }],
      })

      const entry = stream.subagents.value.find((row) => row.agentId === 'task-explore-1')
      const planBlocks = entry?.blocks.filter((block) => block.kind === 'plan') ?? []
      expect(planBlocks).toHaveLength(1)
      expect(planBlocks[0]).toMatchObject({
        kind: 'plan',
        plan: { explanation: 'Reading the source', total: 3 },
      })
      // The subagent's working line facts are its own, from the same payload.
      expect(entry?.plan).toEqual({ done: 0, total: 3, active: 'Reading the source' })

      // The conversation keeps only its own, and its progress line is not
      // overwritten by the worker's checklist.
      const primary = stream.messages.value[stream.messages.value.length - 1]
      expect(primary?.planUpdates).toHaveLength(1)
      expect(primary?.planUpdates?.[0]).toMatchObject({ explanation: 'Checking the target', total: 2 })
      expect(stream.runtimeStatus.value.planTotal).toBe(2)
      expect(stream.runtimeStatus.value.planActive).toBe('Checking the target')

      socket.message({
        op: 'run_completed',
        request_id: sent.request_id,
        text: 'done',
        data: { elapsed_ms: 10 },
      })
      await pendingSend
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', {
        configurable: true,
        writable: true,
        value: originalWebSocket,
      })
    }
  })

  // Thinking is part of the turn that did it, not a turn of its own: the two
  // rows the runtime writes for it are one message whose timeline reads
  // thinking, then the answer - the order the user watched it arrive in.
  it('keeps a reasoning row inside the turn it belongs to', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValueOnce([
      { id: 'reasoning-1', role: 'reasoning', content: 'line 01\nline 02\nline 03' },
      { id: 'assistant-1', role: 'assistant', content: 'done' },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 's1', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's1', records: [] })
    try {
      const stream = useChatStream()
      await stream.loadMessages('s1')

      expect(chatMessagesSpy).toHaveBeenCalledWith('s1', 0)
      expect(stream.messages.value).toHaveLength(1)
      expect(stream.messages.value[0]).toMatchObject({ role: 'assistant', content: 'done' })
      expect(stream.messages.value[0]?.blocks).toEqual([
        { kind: 'thinking', text: 'line 01\nline 02\nline 03', open: false, startedAt: 0 },
        { kind: 'assistant', text: 'done', open: false },
      ])
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('surfaces history load failures and clears them after retry without blanking projected history', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages')
      .mockRejectedValueOnce(new Error('history offline'))
      .mockResolvedValueOnce([
        { id: 'assistant-restored', role: 'assistant', content: 'restored history' },
      ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'retry-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({
      sessionId: 'retry-session', records: [],
    })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'retry-session'
      stream.messages.value = [
        { id: 'assistant-visible', role: 'assistant', content: 'still visible' },
      ]

      await stream.loadMessages('retry-session')

      expect(stream.historyLoading.value).toBe(false)
      expect(stream.historyError.value).toContain('history offline')
      expect(stream.messages.value).toEqual([
        expect.objectContaining({ id: 'assistant-visible', content: 'still visible' }),
      ])

      await stream.loadMessages('retry-session')

      expect(stream.historyLoading.value).toBe(false)
      expect(stream.historyError.value).toBeNull()
      expect(stream.messages.value).toEqual([
        expect.objectContaining({ id: 'assistant-restored', content: 'restored history' }),
      ])
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('does not silently accept a failed canonical event history as a complete legacy replay', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'assistant-restored', role: 'assistant', content: 'restored history' },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents')
      .mockRejectedValueOnce(new Error('canonical history offline'))
      .mockResolvedValueOnce({
        sessionId: 'event-retry-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
      } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({
      sessionId: 'event-retry-session', records: [],
    })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'event-retry-session'
      stream.messages.value = [{ id: 'assistant-visible', role: 'assistant', content: 'still visible' }]

      await stream.loadMessages('event-retry-session')

      expect(stream.historyError.value).toContain('canonical history offline')
      expect(stream.messages.value[0]).toMatchObject({ id: 'assistant-visible', content: 'still visible' })

      await stream.loadMessages('event-retry-session')

      expect(stream.historyError.value).toBeNull()
      expect(stream.messages.value[0]).toMatchObject({ id: 'assistant-restored', content: 'restored history' })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('keeps existing history visible when the server declares an unknown event schema', async () => {
	const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
	  { id: 'replacement', role: 'assistant', content: 'must not replace current history' },
	] as never)
	const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
	  sessionId: 'future-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 2, events: [],
	} as never)
	const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'future-session', records: [] })
	try {
	  const stream = useChatStream()
	  stream.sessionId.value = 'future-session'
	  stream.messages.value = [{ id: 'visible', role: 'assistant', content: 'keep me' }]

	  await stream.loadMessages('future-session')

	  expect(stream.historyError.value).toContain('Unsupported session event schema 2')
	  expect(stream.messages.value).toEqual([{ id: 'visible', role: 'assistant', content: 'keep me' }])
	} finally {
	  chatMessagesSpy.mockRestore()
	  sessionEventsSpy.mockRestore()
	  legacySpy.mockRestore()
	}
  })

  it('replays a complete subagent transcript onto the assistant turn that dispatched it', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'user-1', role: 'user', content: 'first', runId: '' },
      { id: 'assistant-1', role: 'assistant', content: 'first answer', runId: 'parent-1' },
      { id: 'user-2', role: 'user', content: 'second', runId: '' },
      { id: 'assistant-2', role: 'assistant', content: 'second answer', runId: 'parent-2' },
    ])
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'session-1', nextCursor: 8, highWater: 8, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'spawn-1', sequence: 1, schemaVersion: 1, sessionId: 'session-1', runId: 'child-1', type: 'subagent_spawned', createdAt: '2026-09-06T00:00:01Z', payload: { agentId: 'agent-1', agentType: 'explore', taskId: 'task-1', task: 'inspect history', parentRunId: 'parent-1', executionId: 'exec-1' } },
        { id: 'reason-1', sequence: 2, schemaVersion: 1, sessionId: 'session-1', runId: 'child-1', type: 'reasoning_delta', createdAt: '2026-09-06T00:00:02Z', payload: { agentId: 'agent-1', text: 'checking' } },
        { id: 'reason-done-1', sequence: 3, schemaVersion: 1, sessionId: 'session-1', runId: 'child-1', type: 'reasoning_done', createdAt: '2026-09-06T00:00:03Z', payload: { agentId: 'agent-1' } },
        { id: 'tool-1', sequence: 4, schemaVersion: 1, sessionId: 'session-1', runId: 'child-1', type: 'tool_call_completed', createdAt: '2026-09-06T00:00:04Z', payload: { stepId: 'read-1', toolName: 'read_file', displayBody: 'source body', toolMeta: { agentId: 'agent-1', status: 'completed' } } },
        { id: 'approval-1', sequence: 5, schemaVersion: 1, sessionId: 'session-1', runId: 'child-1', type: 'approval_requested', createdAt: '2026-09-06T00:00:05Z', payload: { agentId: 'agent-1', actionId: 'action-1', actionKind: 'shell', message: 'run tests' } },
        { id: 'approval-2', sequence: 6, schemaVersion: 1, sessionId: 'session-1', runId: 'child-1', type: 'approval_resolved', createdAt: '2026-09-06T00:00:06Z', payload: { agentId: 'agent-1', actionId: 'action-1', actionKind: 'shell', decision: 'approved' } },
        { id: 'answer-1', sequence: 7, schemaVersion: 1, sessionId: 'session-1', runId: 'child-1', type: 'assistant_delta', createdAt: '2026-09-06T00:00:07Z', payload: { agentId: 'agent-1', text: 'all checks passed' } },
        { id: 'ended-1', sequence: 8, schemaVersion: 1, sessionId: 'session-1', runId: 'child-1', type: 'subagent_ended', createdAt: '2026-09-06T00:00:08Z', payload: { agentId: 'agent-1', agentType: 'explore', taskId: 'task-1', parentRunId: 'parent-1', executionId: 'exec-1', status: 'ok' } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'session-1', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'session-1'
      await stream.loadMessages('session-1')

      // The run's own turn carries the execution's card — a block of its
      // own, keyed by the execution — and the unrelated turn carries none.
      const first = (stream.messages.value[1].blocks ?? []).find((b) => b.kind === 'subagent')
      expect(first).toMatchObject({ id: 'subagent-exec:exec-1' })
      expect((stream.messages.value[3].blocks ?? []).some((b) => b.kind === 'subagent')).toBe(false)
      expect(stream.subagentCallTasks.get('subagent-exec:exec-1')?.get(0)).toMatchObject({
        agentId: 'agent-1',
        status: 'done',
      })
      const agent = stream.subagents.value.find((record) => record.agentId === 'agent-1')
      expect(agent?.status).toBe('done')
      expect(agent?.blocks.map((block) => block.kind)).toEqual(['prompt', 'thinking', 'tool', 'approval', 'assistant', 'worked'])
      expect(agent?.blocks.some((block) => block.kind === 'approval' && block.status === 'approved')).toBe(true)
      expect(agent?.blocks.find((block) => block.kind === 'thinking')).toMatchObject({
        id: 'reason-1',
        startedAt: Date.parse('2026-09-06T00:00:02Z'),
        durationMs: 1000,
      })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('replays primary run metadata through the same event projection as live updates', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'user-1', role: 'user', content: 'change it' },
      { id: 'assistant-1', role: 'assistant', content: 'changed', runId: 'parent-1' },
    ])
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'session-1', nextCursor: 6, highWater: 6, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'start-1', sequence: 1, schemaVersion: 1, sessionId: 'session-1', runId: 'parent-1', type: 'turn_started', createdAt: '2026-09-06T00:00:01Z', payload: {} },
        { id: 'plan-1', sequence: 2, schemaVersion: 1, sessionId: 'session-1', runId: 'parent-1', type: 'plan_updated', createdAt: '2026-09-06T00:00:02Z', payload: { title: 'Repair plan', completed: 1, total: 2, items: [{ id: 'a', content: 'Persist events', status: 'completed' }] } },
        { id: 'tool-1', sequence: 3, schemaVersion: 1, sessionId: 'session-1', runId: 'parent-1', type: 'tool_call_started', createdAt: '2026-09-06T00:00:03Z', payload: { stepId: 'call-1', toolName: 'shell', description: 'run tests' } },
        { id: 'diff-1', sequence: 4, schemaVersion: 1, sessionId: 'session-1', runId: 'parent-1', type: 'turn_diff_updated', createdAt: '2026-09-06T00:00:04Z', payload: { files: [{ path: 'main.go', status: 'modified', added: 2, deleted: 1 }] } },
        { id: 'answer-1', sequence: 5, schemaVersion: 1, sessionId: 'session-1', runId: 'parent-1', type: 'assistant_delta', createdAt: '2026-09-06T00:00:05Z', payload: { text: 'changed' } },
        { id: 'done-1', sequence: 6, schemaVersion: 1, sessionId: 'session-1', runId: 'parent-1', type: 'turn_completed', createdAt: '2026-09-06T00:00:06Z', payload: { text: 'changed', elapsedMs: 5000 } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'session-1', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'session-1'
      await stream.loadMessages('session-1')

      expect(stream.messages.value[1]).toMatchObject({
        id: 'assistant-1',
        runId: 'parent-1',
        content: 'changed',
        runStartedAt: '2026-09-06T00:00:01Z',
        runFinishedAt: '2026-09-06T00:00:06Z',
        workedDurationMs: 5000,
      })
      expect(stream.messages.value[1]?.planUpdates?.[0]?.title).toBe('Repair plan')
      expect(stream.messages.value[1]?.turnDiffs).toEqual([
        expect.objectContaining({ path: 'main.go', status: 'modified', added: 2, deleted: 1 }),
      ])
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('keeps subagent cards on a distinct turn when the parent produced no answer', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'user-old', role: 'user', content: 'old question' },
      { id: 'assistant-old', role: 'assistant', content: 'old answer', runId: 'old-run' },
      { id: 'user-new', role: 'user', content: 'delegate only' },
    ])
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'session-1', nextCursor: 3, highWater: 3, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'start-new', sequence: 1, schemaVersion: 1, sessionId: 'session-1', runId: 'new-run', type: 'turn_started', createdAt: '2026-09-06T00:01:01Z', payload: {} },
        { id: 'spawn-new', sequence: 2, schemaVersion: 1, sessionId: 'session-1', runId: 'child-new', type: 'subagent_spawned', createdAt: '2026-09-06T00:01:02Z', payload: { agentId: 'agent-new', agentType: 'explore', taskId: 'task-new', task: 'inspect', parentRunId: 'new-run', executionId: 'child-new' } },
        { id: 'done-new', sequence: 3, schemaVersion: 1, sessionId: 'session-1', runId: 'new-run', type: 'turn_completed', createdAt: '2026-09-06T00:01:03Z', payload: { text: '' } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'session-1', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'session-1'
      await stream.loadMessages('session-1')

      const oldTurn = stream.messages.value.find((message) => message.id === 'assistant-old')
      expect((oldTurn?.blocks ?? []).some((b) => b.kind === 'subagent')).toBe(false)
      const host = stream.messages.value.find((message) => message.runId === 'new-run')
      expect(host).toMatchObject({ role: 'assistant', content: '' })
      const standalone = (host?.blocks ?? []).find((b) => b.kind === 'subagent')
      expect(standalone).toMatchObject({ id: 'subagent-exec:child-new' })
      expect((standalone as Extract<TimelineBlock, { kind: 'subagent' }>)?.step.subagentCall?.tasks[0])
        .toMatchObject({ title: 'inspect' })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('restores one thousand primary messages and three hundred complete subagent views', async () => {
	const primaryMessages = Array.from({ length: 1000 }, (_, index) => ({
	  id: `message-${index}`,
	  role: index % 2 === 0 ? 'user' : 'assistant',
	  content: `message body ${index}`,
	  runId: index === 999 ? 'parent-bulk' : (index % 2 === 1 ? `parent-${index}` : ''),
	}))
	const events = Array.from({ length: 300 }, (_, index) => {
	  const number = index + 1
	  const sequence = index * 3
	  const agentId = `agent-${number}`
	  const childRunId = `child-${number}`
	  return [
		{ id: `spawn-${number}`, sequence: sequence + 1, schemaVersion: 1, sessionId: 'bulk-session', runId: childRunId, type: 'subagent_spawned', payload: { agentId, agentType: 'explore', taskId: `task-${number}`, task: `inspect ${number}`, parentRunId: 'parent-bulk', executionId: childRunId } },
		{ id: `answer-${number}`, sequence: sequence + 2, schemaVersion: 1, sessionId: 'bulk-session', runId: childRunId, type: 'assistant_delta', payload: { agentId, text: `result ${number}` } },
		{ id: `ended-${number}`, sequence: sequence + 3, schemaVersion: 1, sessionId: 'bulk-session', runId: childRunId, type: 'subagent_ended', payload: { agentId, agentType: 'explore', taskId: `task-${number}`, parentRunId: 'parent-bulk', executionId: childRunId, status: 'ok' } },
	  ]
	}).flat()
	const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue(primaryMessages as never)
	const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
	  sessionId: 'bulk-session', nextCursor: events.length, highWater: events.length, hasMore: false, schemaVersion: 1, events,
	} as never)
	const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'bulk-session', records: [] })
	try {
	  const stream = useChatStream()
	  stream.sessionId.value = 'bulk-session'
	  await stream.loadMessages('bulk-session')

	  expect(stream.messages.value).toHaveLength(1000)
	  expect(stream.subagents.value).toHaveLength(300)
	  const host = stream.messages.value.find((message) => message.runId === 'parent-bulk')
	  // One card per execution, not two lifecycle lines per subagent.
	  expect((host?.blocks ?? []).filter((b) => b.kind === 'subagent')).toHaveLength(300)
	  expect(stream.subagents.value[0]).toMatchObject({ agentId: 'agent-1', task: 'inspect 1', status: 'done' })
	  expect(stream.subagents.value[0]?.blocks.map((block) => block.kind)).toEqual(['prompt', 'assistant'])
	  expect(stream.subagents.value[299]).toMatchObject({ agentId: 'agent-300', task: 'inspect 300', status: 'done' })
	  const lastBlocks = stream.subagents.value[299]?.blocks ?? []
	  expect(lastBlocks[lastBlocks.length - 1]).toMatchObject({ kind: 'assistant', text: 'result 300' })
	} finally {
	  chatMessagesSpy.mockRestore()
	  sessionEventsSpy.mockRestore()
	  legacySpy.mockRestore()
	}
  })

})

/**
 * Skill message cards. Both entry points a web card can come from — the live
 * WS payload and the persisted tool_meta on reload — must carry the structured
 * skill identity, and the card must label itself from that identity alone,
 * never from argument summaries or activation bodies.
 */
describe('skill message cards', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  const skillMeta = {
    tool_name: 'skill',
    status: 'completed',
    category: 'skill',
    skill_name: 'review-agent',
    skill_path: '/Users/u/.codex/skills/.system/review-agent/SKILL.md',
  }

  it('parses the skill identity from a live WS payload', () => {
    const step = toolStepFromPayload(
      {
        stepId: 'skill-explicit-1',
        toolName: 'skill',
        toolMeta: {
          toolName: 'skill',
          status: 'running',
          category: 'skill',
          skillName: 'review-agent',
          skillPath: '/skills/review-agent/SKILL.md',
        },
      },
      false,
      'fallback-id',
    )
    expect(step.category).toBe('skill')
    expect(step.skillName).toBe('review-agent')
    expect(step.skillPath).toBe('/skills/review-agent/SKILL.md')
    expect(step.status).toBe('running')
  })

  it('parses the skill identity from persisted tool meta on reload', () => {
    const step = toolStepFromRow({
      toolStepId: 'read-skill-9',
      toolMetaJson: JSON.stringify(skillMeta),
      partsJson: JSON.stringify([
        {
          type: 'tool_display',
          body: 'Loaded from /Users/u/.codex/skills/.system/review-agent/SKILL.md',
          summary: 'review-agent',
          tool_meta_json: JSON.stringify(skillMeta),
        },
      ]),
    })
    expect(step.category).toBe('skill')
    expect(step.skillName).toBe('review-agent')
    expect(step.skillPath).toBe('/Users/u/.codex/skills/.system/review-agent/SKILL.md')
    expect(step.output).toBe('Loaded from /Users/u/.codex/skills/.system/review-agent/SKILL.md')
  })

  it('carries the shared display body verbatim, including the raw error', () => {
    const rawError = 'open /skills/review-agent/SKILL.md: permission denied'
    const step = toolStepFromPayload(
      {
        stepId: 'skill-explicit-2',
        toolName: 'skill',
        displayBody: `Failed to load from /skills/review-agent/SKILL.md\n${rawError}`,
        error: rawError,
        toolMeta: { ...skillMeta, status: 'failed' },
      },
      true,
      'fallback-id',
    )
    expect(step.status).toBe('failed')
    expect(step.output).toBe(`Failed to load from /skills/review-agent/SKILL.md\n${rawError}`)
    expect(step.output).toContain('permission denied')
  })

  it('titles the terminal card from the identity, never from arguments or bodies', () => {
    const step = toolStepFromPayload(
      {
        stepId: 'skill-explicit-3',
        toolName: 'skill',
        input: { explicit: true },
        output: '<skill>\n<name>review-agent</name>\nfollow the checklist',
        toolMeta: { ...skillMeta, status: 'completed' },
        summary: 'review-agent {"explicit":true}',
      },
      true,
      'fallback-id',
    )
    const title = skillCardTitle(step)
    expect(title).toBe('Skill review-agent')
    expect(title).not.toContain('{"explicit":true}')
    expect(title).not.toContain('<skill>')
  })

  it('titles the loading card with the progressive form', () => {
    const step = toolStepFromPayload(
      {
        stepId: 'skill-explicit-4',
        toolName: 'skill',
        toolMeta: { ...skillMeta, status: 'running' },
      },
      false,
      'fallback-id',
    )
    expect(skillCardTitle(step)).toBe('Loading skill review-agent')
  })

  it('leaves ordinary tool cards unnamed by the skill title', () => {
    expect(skillCardTitle({ stepId: 't1', toolName: 'shell', summary: 'ran ls', status: 'completed' })).toBeUndefined()
  })
})

/**
 * The conversation's own timeline. A turn's tool calls and the approvals that
 * gated them used to be nowhere on the web: the live handler dropped every call
 * when no plan was running, and reload turned tool rows into assistant bubbles
 * carrying raw tool JSON. Both are checked here against the same block model the
 * subagent view uses.
 */
describe('conversation tool and approval timeline', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  function withFakeWebSocket(run: () => Promise<void> | void) {
    return async () => {
      const originalWebSocket = globalThis.WebSocket
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
      try {
        await run()
      } finally {
        Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
      }
    }
  }

  function runEvent(socket: FakeChatWebSocket, id: string, sequence: number, runId: string, sessionId: string, type: string, payload: Record<string, unknown>) {
    socket.message({
      op: 'run_event',
      data: {
        id,
        sequence,
        type,
        run_id: runId,
        session_id: sessionId,
        created_at: `2026-06-14T00:00:0${sequence}.000Z`,
        payload,
      },
    })
  }

  /**
   * Ends a live turn. The request socket's terminal operation is the legacy
   * run_completed message; the canonical turn_completed event is what the
   * observer folds into the same assistant message.
   */
  function endTurn(socket: FakeChatWebSocket, id: string, sequence: number, runId: string, sessionId: string) {
    runEvent(socket, id, sequence, runId, sessionId, 'turn_completed', { text: 'done' })
    socket.message({ op: 'run_completed', request_id: 'req-probe', run_id: runId, data: { text: 'done' } })
  }

  async function startLiveStream(sessionId: string, runId: string) {
    FakeChatWebSocket.instances = []
    const stream = useChatStream()
    stream.sessionId.value = sessionId
    const pendingSend = stream.send('run the tests')
    const socket = FakeChatWebSocket.instances[0]!
    socket.open()
    // A real socket opens every turn with this, and it is what binds the run to
    // the assistant message the rest of the turn's events land on.
    runEvent(socket, `evt-turn-${runId}`, 0, runId, sessionId, 'turn_started', {})
    return { stream, socket, pendingSend }
  }

  it('shows a tool card from live step events even with no plan running', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-tools', 'run-tools')

    runEvent(socket, 'evt-1', 1, 'run-tools', 'live-tools', 'tool_call_started', { toolName: 'shell', stepId: 'call-1', summary: 'running ls' })
    runEvent(socket, 'evt-2', 2, 'run-tools', 'live-tools', 'tool_call_completed', {
      toolName: 'shell', stepId: 'call-1', summary: 'ran ls', displayBody: 'a.go\nb.go',
    })

    const host = stream.messages.value.find((message) => message.runId === 'run-tools')
    expect(host?.blocks).toHaveLength(1)
    expect(host?.blocks?.[0]).toMatchObject({
      kind: 'tool',
      step: { stepId: 'call-1', toolName: 'shell', status: 'completed', output: 'a.go\nb.go' },
    })
    endTurn(socket, 'evt-3', 3, 'run-tools', 'live-tools')
    await pendingSend
  }))

  it('draws a steer the run handed its model as its own message between two answers', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-delivered', 'run-delivered')

    runEvent(socket, 'evt-1', 1, 'run-delivered', 'live-delivered', 'assistant_delta', { text: 'checking the build' })
    runEvent(socket, 'evt-2', 2, 'run-delivered', 'live-delivered', 'input_delivered', { text: 'also run lint' })
    runEvent(socket, 'evt-3', 3, 'run-delivered', 'live-delivered', 'assistant_delta', { text: 'lint is clean' })

    expect(stream.messages.value.map((message) => [message.role, message.content])).toEqual([
      ['user', 'run the tests'],
      ['assistant', 'checking the build'],
      ['user', 'also run lint'],
      ['assistant', 'lint is clean'],
    ])
    expect(stream.messages.value.slice(1).map((message) => message.runId)).toEqual(['run-delivered', 'run-delivered', 'run-delivered'])

    endTurn(socket, 'evt-done', 4, 'run-delivered', 'live-delivered')
    await pendingSend
    // The run's end settles the answer it was drawing into, not the closed one.
    expect(stream.messages.value.map((message) => [message.role, message.content])).toEqual([
      ['user', 'run the tests'],
      ['assistant', 'checking the build'],
      ['user', 'also run lint'],
      ['assistant', 'lint is clean'],
    ])
  }))

  it('draws a goal as it runs: its lines, its rounds apart, and its check off the conversation', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-goal', 'run-goal')
    const host = () => stream.messages.value.find((message) => message.runId === 'run-goal')

    runEvent(socket, 'evt-1', 1, 'run-goal', 'live-goal', 'goal_started', { objective: 'make the tests pass', max_rounds: 100 })
    runEvent(socket, 'evt-2', 2, 'run-goal', 'live-goal', 'assistant_delta', { text: 'round one' })
    expect(stream.runtimeStatus.value.goalRound).toBe(1)

    runEvent(socket, 'evt-3', 3, 'run-goal', 'live-goal', 'subagent_spawned', {
      agent_id: 'check-1', agent_type: 'goal-evaluator', task_id: 'check-1', title: 'Goal check', task: 'Objective: make the tests pass', execution_id: 'exec-1',
    })
    expect(stream.runtimeStatus.value.goalChecking).toBe(true)
    runEvent(socket, 'evt-4', 4, 'run-goal', 'live-goal', 'subagent_ended', {
      agent_id: 'check-1', agent_type: 'goal-evaluator', task_id: 'check-1', status: 'ok', execution_id: 'exec-1',
    })
    expect(stream.runtimeStatus.value.goalChecking).toBe(false)

    runEvent(socket, 'evt-5', 5, 'run-goal', 'live-goal', 'goal_round_started', { round: 2, why: 'two tests fail', check_agent_id: 'check-1' })
    expect(stream.runtimeStatus.value.goalRound).toBe(2)
    runEvent(socket, 'evt-6', 6, 'run-goal', 'live-goal', 'assistant_delta', { text: 'round two' })
    runEvent(socket, 'evt-7', 7, 'run-goal', 'live-goal', 'goal_completed', {
      objective: 'make the tests pass', status: 'done', rounds: 2, why: 'all pass', duration_ms: 8000, check_agent_id: 'check-2',
    })
    expect(stream.runtimeStatus.value.goalRound).toBeUndefined()

    const blocks = host()?.blocks ?? []
    expect(blocks.map((block) => block.kind === 'goal' ? `goal:${block.goal.phase}` : `${block.kind}:${'text' in block ? block.text : ''}`)).toEqual([
      'goal:started', 'assistant:round one', 'goal:round', 'assistant:round two', 'goal:completed',
    ])
    expect(blocks[2]).toMatchObject({ goal: { round: 2, why: 'two tests fail', checkAgentId: 'check-1' } })
    // The check is the goal's, opened from its lines: no card of its own.
    expect(blocks.some((block) => block.kind === 'subagent')).toBe(false)

    endTurn(socket, 'evt-8', 8, 'run-goal', 'live-goal')
    await pendingSend
  }))

  it('shows a tool call that completes while a plan is running', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-plan-tools', 'run-plan-tools')

    runEvent(socket, 'evt-1', 1, 'run-plan-tools', 'live-plan-tools', 'plan_updated', {
      title: 'Plan', explanation: 'Look around', completed: 0, total: 1,
      items: [{ id: 'a', content: 'Look around', status: 'in_progress' }],
    })
    runEvent(socket, 'evt-2', 2, 'run-plan-tools', 'live-plan-tools', 'tool_call_started', {
      toolName: 'shell', stepId: 'call-1', summary: 'running ls',
    })
    runEvent(socket, 'evt-3', 3, 'run-plan-tools', 'live-plan-tools', 'tool_call_completed', {
      toolName: 'shell', stepId: 'call-1', summary: 'ran ls', displayBody: 'a.go\nb.go',
    })

    // A call is a call: whether a plan happens to be running when it completes
    // cannot decide whether the conversation ever shows the result.
    const host = stream.messages.value.find((message) => message.runId === 'run-plan-tools')
    const toolBlocks = (host?.blocks ?? []).filter((block) => block.kind === 'tool')
    expect(toolBlocks).toHaveLength(1)
    expect(toolBlocks[0]).toMatchObject({
      kind: 'tool',
      step: { stepId: 'call-1', toolName: 'shell', status: 'completed', output: 'a.go\nb.go' },
    })
    endTurn(socket, 'evt-4', 4, 'run-plan-tools', 'live-plan-tools')
    await pendingSend
  }))

  it('turns a conversation approval into a block and stamps the printed line on it', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-approval', 'run-approval')

    runEvent(socket, 'evt-1', 1, 'run-approval', 'live-approval', 'approval_requested', {
      actionId: 'act-1', actionKind: 'exit_plan_mode', toolStepId: 'call-exit',
    })
    let host = stream.messages.value.find((message) => message.runId === 'run-approval')
    expect(host?.blocks?.[0]).toMatchObject({ kind: 'approval', actionId: 'act-1', status: 'pending' })

    runEvent(socket, 'evt-2', 2, 'run-approval', 'live-approval', 'approval_resolved', {
      actionId: 'act-1', actionKind: 'exit_plan_mode', decision: 'cancelled', toolStepId: 'call-exit',
      confirmation: '✗ You canceled forebrain\'s request to exit plan mode',
    })

    host = stream.messages.value.find((message) => message.runId === 'run-approval')
    expect(host?.blocks).toHaveLength(1)
    expect(host?.blocks?.[0]).toMatchObject({
      kind: 'approval',
      actionId: 'act-1',
      status: 'cancelled',
      confirmation: "✗ You canceled forebrain's request to exit plan mode",
    })
    endTurn(socket, 'evt-3', 3, 'run-approval', 'live-approval')
    await pendingSend
  }))

  it('closes a delivered plan review card silently, with no line', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-delivery', 'run-delivery')

    runEvent(socket, 'evt-1', 1, 'run-delivery', 'live-delivery', 'approval_requested', {
      actionId: 'act-1', actionKind: 'exit_plan_mode', toolStepId: 'call-exit',
    })
    // A persisted delivery event replays to a new connection with the
    // internal handoff key as its line: the card still closes, and the line
    // is dropped — the handoff was never something to read.
    runEvent(socket, 'evt-2', 2, 'run-delivery', 'live-delivery', 'approval_resolved', {
      actionId: 'act-1', actionKind: 'exit_plan_mode', decision: 'denied', toolStepId: 'call-exit',
      reason: 'plan-review:delivered',
      confirmation: 'Plan review delivered — the planner is revising the plan.',
    })

    const host = stream.messages.value.find((message) => message.runId === 'run-delivery')
    const block = host?.blocks?.[0]
    expect(block).toMatchObject({
      kind: 'approval',
      actionId: 'act-1',
      status: 'denied',
    })
    if (block?.kind === 'approval') {
      expect(block.confirmation).toBeUndefined()
    }
    endTurn(socket, 'evt-3', 3, 'run-delivery', 'live-delivery')
    await pendingSend
  }))

  it('draws nothing for a delivered gate and its card for a user denial', () => {
    const denialParts = (body: string) => JSON.stringify([
      { type: 'tool_result_meta', tool_call_id: 'call-exit' },
      { type: 'tool_display', body, summary: 'kept planning', tool_meta_json: JSON.stringify({ tool_name: 'exit_plan_mode', status: 'denied' }) },
    ])
    const assistantParts = JSON.stringify([
      { type: 'text', text: '' },
      { type: 'tool_calls', tool_calls: [{ id: 'call-exit', type: 'function', function: { name: 'exit_plan_mode', arguments: '{}' } }] },
    ])

    // A delivered review closed the gate: the row's body is the internal
    // handoff key, so the history draws no card at all.
    const delivered = conversationFromTranscript([
      { id: 'a1', rowId: 1, role: 'assistant', content: '', partsJson: assistantParts },
      { id: 't1', rowId: 2, role: 'tool', content: '{}', toolStepId: 'call-exit', partsJson: denialParts('Plan review delivered — the planner is revising the plan.') },
    ] as unknown as Parameters<typeof conversationFromTranscript>[0])
    expect(delivered.flatMap((message) => (message.blocks ?? []).filter((block) => block.kind === 'tool'))).toEqual([])

    // The user's own denial keeps its card — their words are history.
    const denied = conversationFromTranscript([
      { id: 'a1', rowId: 1, role: 'assistant', content: '', partsJson: assistantParts },
      { id: 't1', rowId: 2, role: 'tool', content: '{}', toolStepId: 'call-exit', partsJson: denialParts('Please split the plan into milestones.') },
    ] as unknown as Parameters<typeof conversationFromTranscript>[0])
    expect(denied.flatMap((message) => (message.blocks ?? []).filter((block) => block.kind === 'tool'))).toHaveLength(1)
  })

  it('drops a canceled gate row whole and still shows the confirmation line', async () => {
    const gateRow = (status: string) => ({
      id: 't1', rowId: 3, role: 'tool', content: '{}', toolStepId: 'call-exit',
      partsJson: JSON.stringify([
        { type: 'tool_result_meta', tool_call_id: 'call-exit' },
        { type: 'tool_display', body: 'plan review closed the gate', summary: '', tool_meta_json: JSON.stringify({ tool_name: 'exit_plan_mode', status }) },
      ]),
    })
    const assistantParts = JSON.stringify([
      { type: 'text', text: 'here is the plan' },
      { type: 'tool_calls', tool_calls: [{ id: 'call-exit', type: 'function', function: { name: 'exit_plan_mode', arguments: '{}' } }] },
    ])

    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'u1', rowId: 1, role: 'user', content: 'plan the work' },
      { id: 'a1', rowId: 2, role: 'assistant', content: 'here is the plan', runId: 'run-1', partsJson: assistantParts },
      gateRow('canceled'),
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'cancel-gate-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'evt-1', sequence: 1, schemaVersion: 1, runId: 'run-1', sessionId: 'cancel-gate-session', type: 'approval_resolved', payload: { actionId: 'act-1', actionKind: 'exit_plan_mode', decision: 'cancelled', toolStepId: 'call-exit', confirmation: "✗ You canceled forebrain's request to exit plan mode" } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'cancel-gate-session', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'cancel-gate-session'
      await stream.loadMessages('cancel-gate-session')

      // The walk-away's only record is the confirmation line: no card for a
      // gate that never answered, the line where its turn ended.
      const turn = stream.messages.value[1]
      expect(turn?.blocks?.map((block) => block.kind)).toEqual(['assistant', 'approval'])
      expect(turn?.blocks?.[1]).toMatchObject({
        kind: 'approval',
        status: 'cancelled',
        confirmation: "✗ You canceled forebrain's request to exit plan mode",
      })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('keeps a denied gate row card from history', async () => {
    const assistantParts = JSON.stringify([
      { type: 'text', text: 'here is the plan' },
      { type: 'tool_calls', tool_calls: [{ id: 'call-exit', type: 'function', function: { name: 'exit_plan_mode', arguments: '{}' } }] },
    ])
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'u1', rowId: 1, role: 'user', content: 'plan the work' },
      { id: 'a1', rowId: 2, role: 'assistant', content: 'here is the plan', runId: 'run-1', partsJson: assistantParts },
      {
        id: 't1', rowId: 3, role: 'tool', content: '{}', toolStepId: 'call-exit',
        partsJson: JSON.stringify([
          { type: 'tool_result_meta', tool_call_id: 'call-exit' },
          { type: 'tool_display', body: 'Please split the plan into milestones.', summary: 'kept planning', tool_meta_json: JSON.stringify({ tool_name: 'exit_plan_mode', status: 'denied' }) },
        ]),
      },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'deny-gate-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'deny-gate-session', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'deny-gate-session'
      await stream.loadMessages('deny-gate-session')

      // The user's denial is a settled answer: the card stays.
      const turn = stream.messages.value[1]
      expect(turn?.blocks?.map((block) => block.kind)).toEqual(['assistant', 'tool'])
      expect(turn?.blocks?.[1]).toMatchObject({ kind: 'tool', step: { stepId: 'call-exit', status: 'denied' } })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('keeps a subagent approval out of the conversation timeline', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-subagent-approval', 'run-subagent-approval')

    runEvent(socket, 'evt-1', 1, 'run-subagent-approval', 'live-subagent-approval', 'subagent_spawned', {
      agentId: 'agent-1', agentType: 'explore', taskId: 'agent-1', task: 'inspect',
    })
    runEvent(socket, 'evt-2', 2, 'run-subagent-approval', 'live-subagent-approval', 'approval_requested', {
      actionId: 'child-act', actionKind: 'shell', agentId: 'agent-1',
    })
    runEvent(socket, 'evt-3', 3, 'run-subagent-approval', 'live-subagent-approval', 'approval_resolved', {
      actionId: 'child-act', actionKind: 'shell', agentId: 'agent-1', decision: 'approved',
      confirmation: '✔ You approved forebrain to run ls',
    })

    expect(stream.subagents.value[0]?.blocks.some((block) => block.kind === 'approval')).toBe(true)
    // The conversation's account of the subagent is the one card of its
    // execution — never its approval, its calls, or its answer.
    const conversationBlocks = stream.messages.value.flatMap((message) => message.blocks ?? [])
    expect(conversationBlocks.filter((block) => block.kind === 'approval')).toEqual([])
    expect(conversationBlocks.filter((block) => block.kind === 'subagent')).toHaveLength(1)
    endTurn(socket, 'evt-4', 4, 'run-subagent-approval', 'live-subagent-approval')
    await pendingSend
  }))

  /**
   * The one assertion that keeps the two fill paths honest. A live turn is built
   * from run events as they arrive; a reloaded one is rebuilt from the rows the
   * runtime wrote for it. They are different inputs to the same conversation, so
   * they have to produce the same timeline - otherwise a refresh shows the user
   * a different conversation from the one they were just watching, which is how
   * the two paths drifted in the first place.
   */
  it('reloads a turn into the same timeline the live stream built', withFakeWebSocket(async () => {
    const timeline = (messages: ReturnType<typeof useChatStream>['messages']['value']) =>
      messages.map((message) => ({
        role: message.role,
        content: message.content,
        blocks: (message.blocks ?? []).map((block) => {
          switch (block.kind) {
            case 'tool':
              return { kind: block.kind, stepId: block.step.stepId, status: block.step.status, output: block.step.output }
            case 'approval':
              return { kind: block.kind, actionId: block.actionId, status: block.status, confirmation: block.confirmation }
            case 'assistant':
            case 'thinking':
              return { kind: block.kind, text: block.text }
            default:
              return { kind: block.kind }
          }
        }),
      }))

    // The live half is driven exactly as the gateway drives it: the legacy
    // operations the request socket still carries, and the canonical events that
    // mirror some of them and stand alone for the rest — the answer among them.
    // Sending both is what proves the mirrored ones are applied once and the
    // rest are applied at all.
    FakeChatWebSocket.instances = []
    const live = useChatStream()
    live.sessionId.value = 'parity-session'
    const pendingSend = live.send('run the tests')
    const socket = FakeChatWebSocket.instances[0]!
    socket.open()
    socket.message({ op: 'run_started', run_id: 'run-parity', session_id: 'parity-session' })
    runEvent(socket, 'evt-0', 0, 'run-parity', 'parity-session', 'turn_started', {})
    runEvent(socket, 'evt-1', 1, 'run-parity', 'parity-session', 'reasoning_delta', { text: 'weighing it up' })
    runEvent(socket, 'evt-2', 2, 'run-parity', 'parity-session', 'assistant_delta', { text: 'let me check' })
    runEvent(socket, 'evt-3', 3, 'run-parity', 'parity-session', 'tool_call_started', { toolName: 'shell', stepId: 'call-1', summary: 'running ls' })
    runEvent(socket, 'evt-4', 4, 'run-parity', 'parity-session', 'tool_call_completed', {
      toolName: 'shell', stepId: 'call-1', summary: 'ran ls', displayBody: 'a.go',
    })
    runEvent(socket, 'evt-5', 5, 'run-parity', 'parity-session', 'assistant_delta', { text: 'now the plan' })
    runEvent(socket, 'evt-6', 6, 'run-parity', 'parity-session', 'tool_call_started', {
      toolName: 'exit_plan_mode', stepId: 'call-exit', summary: 'running exit_plan_mode', toolMeta: { tool_name: 'exit_plan_mode', status: 'awaiting approval' },
    })
    runEvent(socket, 'evt-7', 7, 'run-parity', 'parity-session', 'approval_requested', {
      actionId: 'act-1', actionKind: 'exit_plan_mode', toolStepId: 'call-exit',
    })
    runEvent(socket, 'evt-8', 8, 'run-parity', 'parity-session', 'approval_resolved', {
      actionId: 'act-1', actionKind: 'exit_plan_mode', decision: 'cancelled', toolStepId: 'call-exit',
      confirmation: '✗ You canceled forebrain\'s request to exit plan mode',
    })
    runEvent(socket, 'evt-9', 9, 'run-parity', 'parity-session', 'turn_cancelled', { message: 'cancelled' })
    socket.message({ op: 'run_cancelled', run_id: 'run-parity', message: 'cancelled' })
    await pendingSend
    // A stopped run closes with its worked line as surely as a finished one.
    expect(live.messages.value[live.messages.value.length - 1]).toMatchObject({ role: 'assistant', workedDurationMs: expect.any(Number) })

    // The same turn as the runtime persisted it: what it thought, what it said,
    // the call it answered, and the call the cancel stopped.
    const rows = [
      { id: 'u1', rowId: 1, role: 'user', content: 'run the tests' },
      { id: 'r1', rowId: 2, role: 'reasoning', content: 'weighing it up' },
      {
        id: 'a1', rowId: 3, role: 'assistant', content: 'let me check', runId: 'run-parity',
        partsJson: JSON.stringify([
          { type: 'text', text: 'let me check' },
          { type: 'tool_calls', tool_calls: [{ id: 'call-1', type: 'function', function: { name: 'shell', arguments: '{"command":"ls"}' } }] },
        ]),
      },
      {
        id: 't1', rowId: 4, role: 'tool', content: '{"stdout":"a.go"}', toolStepId: 'call-1',
        partsJson: JSON.stringify([
          { type: 'tool_result_meta', tool_call_id: 'call-1' },
          { type: 'tool_display', body: 'a.go', summary: 'ran ls', tool_meta_json: JSON.stringify({ tool_name: 'shell', status: 'completed' }) },
        ]),
      },
      {
        id: 'a2', rowId: 5, role: 'assistant', content: 'now the plan', runId: 'run-parity',
        partsJson: JSON.stringify([
          { type: 'text', text: 'now the plan' },
          { type: 'tool_calls', tool_calls: [{ id: 'call-exit', type: 'function', function: { name: 'exit_plan_mode', arguments: '{}' } }] },
        ]),
      },
    ]
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue(rows as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'parity-session', nextCursor: 2, highWater: 2, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'evt-7', sequence: 7, schemaVersion: 1, runId: 'run-parity', sessionId: 'parity-session', type: 'approval_requested', payload: { actionId: 'act-1', actionKind: 'exit_plan_mode', toolStepId: 'call-exit' } },
        { id: 'evt-8', sequence: 8, schemaVersion: 1, runId: 'run-parity', sessionId: 'parity-session', type: 'approval_resolved', payload: { actionId: 'act-1', actionKind: 'exit_plan_mode', decision: 'cancelled', toolStepId: 'call-exit', confirmation: '✗ You canceled forebrain\'s request to exit plan mode' } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'parity-session', records: [] })
    try {
      const reloaded = useChatStream()
      reloaded.sessionId.value = 'parity-session'
      await reloaded.loadMessages('parity-session')

      expect(timeline(reloaded.messages.value)).toEqual(timeline(live.messages.value))
      // And it is the timeline both are supposed to have, not two matching
      // empties: what it thought, what it said, the call it ran, what it said
      // next, and the line the gate printed. The gate itself holds no card on
      // either path — its prompt is the whole wait — so its record lands above
      // the turn's last call, where the terminal prints the line.
      expect(timeline(live.messages.value).map((message) => message.role)).toEqual(['user', 'assistant'])
      expect(timeline(live.messages.value)[1]?.blocks.map((block) => block.kind))
        .toEqual(['thinking', 'assistant', 'approval', 'tool', 'assistant'])
      expect(timeline(live.messages.value)[1]?.content).toBe('let me check\n\nnow the plan')
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  }))

  it('rebuilds the same cards from history as the live stream showed', async () => {
    const assistantParts = JSON.stringify([
      { type: 'text', text: 'let me check' },
      { type: 'tool_calls', tool_calls: [
        { id: 'call-1', type: 'function', function: { name: 'shell', arguments: '{"command":"ls"}' } },
        { id: 'call-2', type: 'function', function: { name: 'shell', arguments: '{"command":"go test ./..."}' } },
      ] },
    ])
    const toolParts = JSON.stringify([
      { type: 'tool_result_meta', tool_call_id: 'call-1' },
      { type: 'tool_display', body: 'a.go\nb.go', summary: 'ran ls', tool_meta_json: JSON.stringify({ toolName: 'shell', status: 'completed', invocation: 'ls' }) },
    ])
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'u1', rowId: 1, role: 'user', content: 'plan the work' },
      { id: 'a1', rowId: 2, role: 'assistant', content: 'let me check', runId: 'run-1', partsJson: assistantParts },
      { id: 't1', rowId: 3, role: 'tool', content: '{"stdout":"a.go"}', toolStepId: 'call-1', partsJson: toolParts },
      { id: 'a2', rowId: 4, role: 'assistant', content: '', partsJson: JSON.stringify([
        { type: 'tool_calls', tool_calls: [{ id: 'call-grep', type: 'function', function: { name: 'read_file', arguments: '{"file_path":"docs/plan/do-the-thing.md"}' } }] },
      ]) },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'reload-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'reload-session', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'reload-session'
      await stream.loadMessages('reload-session')

      // Every row the runtime wrote for this turn is one message: what it said,
      // then the calls it made, in the order it made them. The completed call
      // carries the output the surface showed; the calls the run never answered
      // become canceled cards rather than stray bubbles.
      expect(stream.messages.value.map((message) => message.role)).toEqual(['user', 'assistant'])
      const turn = stream.messages.value[1]
      expect(turn?.blocks?.map((block) => block.kind)).toEqual(['assistant', 'tool', 'tool', 'tool'])
      expect(turn?.blocks?.[0]).toMatchObject({ kind: 'assistant', text: 'let me check' })
      expect(turn?.blocks?.[1]).toMatchObject({ kind: 'tool', step: { stepId: 'call-1', status: 'completed', output: 'a.go\nb.go' } })
      expect(turn?.blocks?.[2]).toMatchObject({ kind: 'tool', step: { stepId: 'call-2', status: 'canceled', summary: 'Canceled · go test ./...' } })
      expect(turn?.blocks?.[3]).toMatchObject({ kind: 'tool', step: { stepId: 'call-grep', status: 'canceled' } })
      // The row that only issued a call contributes its card and no prose.
      expect(turn?.content).toBe('let me check')
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('never renders a tool row as a message of its own', async () => {
    const toolParts = JSON.stringify([
      { type: 'tool_result_meta', tool_call_id: 'call-1' },
      { type: 'tool_display', body: 'formatted output', summary: 'ran ls', tool_meta_json: JSON.stringify({ toolName: 'shell', status: 'completed', invocation: 'ls' }) },
    ])
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'a1', rowId: 1, role: 'assistant', content: 'running it', partsJson: JSON.stringify([
        { type: 'tool_calls', tool_calls: [{ id: 'call-1', type: 'function', function: { name: 'shell', arguments: '{"command":"ls"}' } }] },
      ]) },
      { id: 't1', rowId: 2, role: 'tool', content: '{"stdout":"a.go","exit_code":0}', toolStepId: 'call-1', partsJson: toolParts },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'tool-row-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'tool-row-session', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'tool-row-session'
      await stream.loadMessages('tool-row-session')

      expect(stream.messages.value.map((message) => message.role)).toEqual(['assistant'])
      for (const message of stream.messages.value) {
        expect(message.content).not.toContain('exit_code')
        expect(message.content).not.toContain('stdout')
      }
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('keeps one card per call id when the same call is persisted twice', async () => {
    const duplicatedAssistant = JSON.stringify([
      { type: 'tool_calls', tool_calls: [{ id: 'call-1', type: 'function', function: { name: 'shell', arguments: '{"command":"ls"}' } }] },
    ])
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'a1', rowId: 1, role: 'assistant', content: 'here is the plan', partsJson: duplicatedAssistant },
      { id: 'a2', rowId: 2, role: 'assistant', content: 'here is the plan', partsJson: duplicatedAssistant },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'dup-session', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'dup-session', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'dup-session'
      await stream.loadMessages('dup-session')

      const cards = stream.messages.value.flatMap((message) => message.blocks ?? []).filter((block) => block.kind === 'tool')
      expect(cards).toHaveLength(1)
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  const emptyPreview = { pendingSteers: [], rejectedSteers: [], queuedMessages: [] }

  it('queues a message sent during a run whole, and sends it whole when the run hands it back', withFakeWebSocket(async () => {
    const queued = vi.spyOn(forebrainApi, 'runQueuedInput').mockResolvedValue({ accepted: true, preview: { ...emptyPreview, queuedMessages: ['look at this'] } })
    const steered = vi.spyOn(forebrainApi, 'runInput').mockResolvedValue({ accepted: true } as never)
    const info = vi.spyOn(forebrainApi, 'fileInfo').mockResolvedValue({ id: 'file-1', originalName: 'spec.pdf', mediaType: 'application/pdf' })
    try {
      const { stream, socket, pendingSend } = await startLiveStream('live-queue', 'run-queue')

      // Plan 004 (decision D7): a message with attachments steers like any
      // other — what it attaches travels in the queue entry and comes back
      // whole, so it no longer waits for the next turn.
      await stream.send('look at this', {
        attached: { attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }], mentionImages: ['shots/a.png'] },
        activeInputDisposition: 'steer',
      })
      expect(steered).toHaveBeenCalledWith('run-queue', { message: 'look at this', attachments: ['file-1'], mentionImages: ['shots/a.png'] })
      expect(queued).not.toHaveBeenCalled()

      // The run ends and hands back what the engine decided follows it: the
      // entries in next run as the following turn, still whole.
      runEvent(socket, 'evt-released', 1, 'run-queue', 'live-queue', 'queued_input_released', {
        next: [{ text: 'look at this', attachments: ['file-1'], mention_images: ['shots/a.png'] }],
        inputs: [],
      })
      expect(stream.pendingInputPreview.value.queuedMessages).toEqual([])
      endTurn(socket, 'evt-done', 2, 'run-queue', 'live-queue')
      await vi.waitFor(() => expect(FakeChatWebSocket.instances).toHaveLength(2))
      const next = FakeChatWebSocket.instances[1]!
      next.open()
      expect(JSON.parse(next.sent[0] ?? '{}')).toMatchObject({
        op: 'start_run',
        message: { content: 'look at this', attachments: ['file-1'], mention_images: ['shots/a.png'] },
      })
      runEvent(next, 'evt-next', 3, 'run-next', 'live-queue', 'turn_started', {})
      endTurn(next, 'evt-next-done', 4, 'run-next', 'live-queue')
      await pendingSend
      expect(stream.takeReturnedDraft()).toBeNull()
    } finally {
      queued.mockRestore()
      steered.mockRestore()
      info.mockRestore()
    }
  }))

  it('gives back what a failed run hands back instead of sending it', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-failed', 'run-failed')
    runEvent(socket, 'evt-released', 1, 'run-failed', 'live-failed', 'queued_input_released', {
      inputs: [{ text: 'after that', attachments: [], mention_images: ['shots/a.png'] }],
    })
    socket.message({ op: 'run_error', request_id: 'req-probe', run_id: 'run-failed', error: 'the provider refused the request' })
    await pendingSend
    expect(FakeChatWebSocket.instances).toHaveLength(1)
    expect(stream.takeReturnedDraft()).toEqual({ text: 'after that', attachments: [], mentionImages: ['shots/a.png'] })
  }))

  it('follows a run resumed after an approval to its end, then sends what it handed back', withFakeWebSocket(async () => {
    const { stream, socket, pendingSend } = await startLiveStream('live-parked', 'run-parked')
    socket.message({ op: 'run_started', request_id: 'req-probe', run_id: 'run-parked', session_id: 'live-parked' })
    socket.message({ op: 'requires_action', request_id: 'req-probe', run_id: 'run-parked', session_id: 'live-parked', data: { action_id: 'act-1' } })

    // After the decision the gateway resumes the run on its own; its account
    // arrives only as events, and every one of them counts.
    runEvent(socket, 'evt-resumed-text', 1, 'run-parked', 'live-parked', 'assistant_delta', { text: 'resumed answer' })
      runEvent(socket, 'evt-released', 2, 'run-parked', 'live-parked', 'queued_input_released', {
        next: [{ text: 'next one', attachments: [], mention_images: [] }],
        inputs: [],
      })
    runEvent(socket, 'evt-completed', 3, 'run-parked', 'live-parked', 'turn_completed', { text: 'resumed answer' })

    const host = stream.messages.value.find((message) => message.runId === 'run-parked')
    expect(host?.blocks?.some((block) => block.kind === 'assistant' && block.text.includes('resumed answer'))).toBe(true)
    await vi.waitFor(() => expect(FakeChatWebSocket.instances).toHaveLength(2))
    const next = FakeChatWebSocket.instances[1]!
    next.open()
    expect(JSON.parse(next.sent[0] ?? '{}')).toMatchObject({ op: 'start_run', message: { content: 'next one' } })
    runEvent(next, 'evt-next', 4, 'run-next', 'live-parked', 'turn_started', {})
    endTurn(next, 'evt-next-done', 5, 'run-next', 'live-parked')
    await pendingSend
  }))

  /** Reads for the conversation a switch lands on: nothing to draw, nothing to ask. */
  function mockEmptySessionFetches(sessionId: string) {
    vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([] as never)
    vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId, nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
    } as never)
    vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId, records: [] })
  }

  it('sends what a new conversation\'s first run hands back into that conversation', withFakeWebSocket(async () => {
    const queued = vi.spyOn(forebrainApi, 'runQueuedInput').mockResolvedValue({ accepted: true, preview: { ...emptyPreview, queuedMessages: ['and this'] } })
    const info = vi.spyOn(forebrainApi, 'fileInfo').mockResolvedValue({ id: 'file-1', originalName: 'spec.pdf', mediaType: 'application/pdf' })
    try {
      FakeChatWebSocket.instances = []
      const stream = useChatStream()
      const pendingSend = stream.send('first question')
      const socket = FakeChatWebSocket.instances[0]!
      socket.open()
      socket.message({ op: 'session_bound', request_id: 'req-probe', session_id: 'bound-conversation' })
      runEvent(socket, 'evt-turn', 1, 'run-bound', 'bound-conversation', 'turn_started', {})
      await stream.send('and this', {
        attached: { attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }], mentionImages: [] },
        activeInputDisposition: 'queue',
      })
      expect(queued).toHaveBeenCalledWith('run-bound', { message: 'and this', attachments: ['file-1'], mentionImages: undefined })

      runEvent(socket, 'evt-released', 2, 'run-bound', 'bound-conversation', 'queued_input_released', {
        next: [{ text: 'and this', attachments: ['file-1'], mention_images: [] }],
        inputs: [],
      })
      endTurn(socket, 'evt-done', 3, 'run-bound', 'bound-conversation')

      // The conversation the socket bound while the user stayed in it is the
      // one the follow-up runs in: the entry send had no session of its own.
      await vi.waitFor(() => expect(FakeChatWebSocket.instances).toHaveLength(3))
      const next = FakeChatWebSocket.instances[2]!
      next.open()
      expect(JSON.parse(next.sent[0] ?? '{}')).toMatchObject({
        op: 'start_run',
        session_id: 'bound-conversation',
        message: { content: 'and this', attachments: ['file-1'] },
      })
      runEvent(next, 'evt-next', 4, 'run-next', 'bound-conversation', 'turn_started', {})
      endTurn(next, 'evt-next-done', 5, 'run-next', 'bound-conversation')
      await pendingSend
      expect(stream.takeReturnedDraft()).toBeNull()
    } finally {
      queued.mockRestore()
      info.mockRestore()
    }
  }))

  it('returns what a run hands back to the composer when its conversation was left, instead of sending it into the one on screen', withFakeWebSocket(async () => {
    const queued = vi.spyOn(forebrainApi, 'runQueuedInput').mockResolvedValue({ accepted: true, preview: { ...emptyPreview, queuedMessages: ['look at this'] } })
    const info = vi.spyOn(forebrainApi, 'fileInfo').mockResolvedValue({ id: 'file-1', originalName: 'spec.pdf', mediaType: 'application/pdf' })
    try {
      const { stream, socket, pendingSend } = await startLiveStream('left-queue', 'run-left')
      await stream.send('look at this', {
        attached: { attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }], mentionImages: ['shots/a.png'] },
        activeInputDisposition: 'queue',
      })
      expect(queued).toHaveBeenCalledWith('run-left', { message: 'look at this', attachments: ['file-1'], mentionImages: ['shots/a.png'] })

      // The user opens another conversation before the run ends and hands
      // back what it never took.
      mockEmptySessionFetches('left-elsewhere')
      stream.switchToSession('left-elsewhere')

      runEvent(socket, 'evt-released', 1, 'run-left', 'left-queue', 'queued_input_released', {
        inputs: [{ text: 'look at this', attachments: ['file-1'], mention_images: ['shots/a.png'] }],
      })
      endTurn(socket, 'evt-done', 2, 'run-left', 'left-queue')
      await pendingSend

      // Back to the composer whole, and no second run started anywhere: the
      // messages belong to the conversation that was left, not the one on
      // screen. The hand-back resolves its attachments asynchronously.
      await vi.waitFor(() => expect(stream.takeReturnedDraft()).toEqual({ text: 'look at this', attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }], mentionImages: ['shots/a.png'] }))
      expect(FakeChatWebSocket.instances.filter((s) => s.sent.some((m) => m.includes('"op":"start_run"')))).toHaveLength(1)
    } finally {
      queued.mockRestore()
      info.mockRestore()
      vi.restoreAllMocks()
    }
  }))

  it('keeps a later send\'s stream intact when a send left running in another conversation ends', withFakeWebSocket(async () => {
    try {
      FakeChatWebSocket.instances = []
      const stream = useChatStream()
      stream.sessionId.value = 'race-a'
      const firstSend = stream.send('a question')
      const socketA = FakeChatWebSocket.instances[0]!
      socketA.open()

      // The user opens another conversation and starts a run there while the
      // first send's socket is still out.
      mockEmptySessionFetches('race-b')
      stream.switchToSession('race-b')
      const secondSend = stream.send('b question')
      const socketB = FakeChatWebSocket.instances[2]!
      socketB.open()

      // The left send's socket drops: its cleanup must not end the run on
      // screen — not the working line, not the composer's lock.
      socketA.close()
      await firstSend
      expect(stream.isStreaming.value).toBe(true)
      expect(stream.runtimeStatus.value.kind).toBe('working')

      socketB.message({ op: 'run_started', request_id: 'req-probe', run_id: 'run-b', session_id: 'race-b' })
      expect(stream.contextSignals.value.activeRunId).toBe('run-b')
      const runCancel = vi.spyOn(forebrainApi, 'runCancel').mockResolvedValue({} as never)
      stream.cancel()
      expect(runCancel).toHaveBeenCalledWith('run-b')
      expect(socketB.readyState).toBe(FakeChatWebSocket.OPEN)

      socketB.message({ op: 'run_completed', request_id: 'req-probe', run_id: 'run-b', data: { text: 'done' } })
      await secondSend
      expect(stream.isStreaming.value).toBe(false)
      runCancel.mockRestore()
    } finally {
      vi.restoreAllMocks()
    }
  }))

  it('keeps a later send\'s stream intact when the send left in another conversation ends by its own terminal op', withFakeWebSocket(async () => {
    try {
      FakeChatWebSocket.instances = []
      const stream = useChatStream()
      stream.sessionId.value = 'race-a'
      const firstSend = stream.send('a question')
      const socketA = FakeChatWebSocket.instances[0]!
      socketA.open()

      // The user opens another conversation and starts a run there while the
      // first send's socket is still out.
      mockEmptySessionFetches('race-b')
      stream.switchToSession('race-b')
      const secondSend = stream.send('b question')
      const socketB = FakeChatWebSocket.instances[2]!
      socketB.open()

      // The left send's run ends the legacy way: its own socket's terminal
      // operation, not a disconnect. It must not unlock the run on screen —
      // that stream belongs to the send holding the controller now.
      socketA.message({ op: 'run_completed', request_id: 'req-probe', run_id: 'run-a', data: { text: 'a done' } })
      await firstSend
      expect(stream.isStreaming.value).toBe(true)
      expect(stream.runtimeStatus.value.kind).toBe('working')

      // The run on screen can still be stopped, and still ends on its own.
      socketB.message({ op: 'run_started', request_id: 'req-probe', run_id: 'run-b', session_id: 'race-b' })
      expect(stream.contextSignals.value.activeRunId).toBe('run-b')
      const runCancel = vi.spyOn(forebrainApi, 'runCancel').mockResolvedValue({} as never)
      stream.cancel()
      expect(runCancel).toHaveBeenCalledWith('run-b')
      expect(socketB.readyState).toBe(FakeChatWebSocket.OPEN)

      socketB.message({ op: 'run_completed', request_id: 'req-probe', run_id: 'run-b', data: { text: 'done' } })
      await secondSend
      expect(stream.isStreaming.value).toBe(false)
      runCancel.mockRestore()
    } finally {
      vi.restoreAllMocks()
    }
  }))

  /** A send whose conversation is being compacted for it, before its run exists. */
  function startWaitingSend(sessionId: string) {
    FakeChatWebSocket.instances = []
    const stream = useChatStream()
    stream.sessionId.value = sessionId
    const first = stream.send('first question', {
      attached: { attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }], mentionImages: [] },
    })
    const socket = FakeChatWebSocket.instances[0]!
    socket.open()
    const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
    runEvent(socket, 'evt-compacting', 1, '', sessionId, 'context_compacting', { compaction_id: 'c-pre', trigger: 'auto', tokens_before: 1506 })
    return { stream, socket, first, requestId: String(sent.request_id) }
  }

  it('holds a message sent while the send before it waits for its run, then hands it to that run', withFakeWebSocket(async () => {
    const steered = vi.spyOn(forebrainApi, 'runInput').mockResolvedValue({ accepted: true, preview: { ...emptyPreview, pendingSteers: ['also this'] } })
    try {
      const { stream, socket, first, requestId } = startWaitingSend('held-run')

      const held = stream.send('also this', { activeInputDisposition: 'steer' })
      // One send at a time: the second waits behind the first, and shows as queued.
      expect(FakeChatWebSocket.instances).toHaveLength(1)
      expect(stream.pendingInputPreview.value.queuedMessages).toEqual(['also this'])

      socket.message({ op: 'run_started', request_id: requestId, run_id: 'run-held', session_id: 'held-run' })
      await held
      expect(steered).toHaveBeenCalledWith('run-held', { message: 'also this' })
      expect(stream.pendingInputPreview.value.queuedMessages).toEqual([])

      runEvent(socket, 'evt-started', 2, 'run-held', 'held-run', 'turn_started', {})
      endTurn(socket, 'evt-done', 3, 'run-held', 'held-run')
      await first
    } finally {
      steered.mockRestore()
    }
  }))

  it('gives a held message back after the withdrawn send it was waiting behind', withFakeWebSocket(async () => {
    const { stream, socket, first, requestId } = startWaitingSend('held-withdrawn')
    const held = stream.send('and this', { attached: { attachments: [], mentionImages: ['shots/a.png'] } })

    stream.cancel()
    runEvent(socket, 'evt-cancelled', 2, '', 'held-withdrawn', 'context_compact_failed', { compaction_id: 'c-pre', trigger: 'auto', cancelled: true })
    socket.message({ op: 'turn_withdrawn', request_id: requestId, session_id: 'held-withdrawn' })

    // One draft, in the order it was written: the withdrawn message, then the
    // one sent while it waited.
    await first
    await held
    expect(stream.takeReturnedDraft()).toEqual({
      text: 'first question\nand this',
      attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }],
      mentionImages: ['shots/a.png'],
    })
    expect(stream.pendingInputPreview.value.queuedMessages).toEqual([])
  }))

  it('recalls a held message before its run exists', withFakeWebSocket(async () => {
    const queued = vi.spyOn(forebrainApi, 'runQueuedInput')
    try {
      const { stream, socket, first, requestId } = startWaitingSend('held-recall')
      const held = stream.send('never mind', { attached: { attachments: [], mentionImages: ['shots/a.png'] } })

      await expect(stream.editLastQueuedMessage()).resolves.toEqual({ text: 'never mind', attachments: [], mentionImages: ['shots/a.png'] })
      await held
      expect(stream.takeReturnedDraft()).toBeNull()
      expect(queued).not.toHaveBeenCalled()
      expect(stream.pendingInputPreview.value.queuedMessages).toEqual([])

      socket.message({ op: 'run_started', request_id: requestId, run_id: 'run-recall', session_id: 'held-recall' })
      runEvent(socket, 'evt-started', 2, 'run-recall', 'held-recall', 'turn_started', {})
      endTurn(socket, 'evt-done', 3, 'run-recall', 'held-recall')
      await first
    } finally {
      queued.mockRestore()
    }
  }))

  it('gives back a message a run that just ended no longer takes', withFakeWebSocket(async () => {
    const queued = vi.spyOn(forebrainApi, 'runQueuedInput').mockResolvedValue({ accepted: false, preview: emptyPreview })
    try {
      const { stream, socket, pendingSend } = await startLiveStream('live-late', 'run-late')
      await stream.send('too late', { activeInputDisposition: 'queue' })
      expect(stream.takeReturnedDraft()).toEqual({ text: 'too late', attachments: [], mentionImages: [] })
      endTurn(socket, 'evt-done', 1, 'run-late', 'live-late')
      await pendingSend
    } finally {
      queued.mockRestore()
    }
  }))

  it('recalls the newest queued message whole, with its attachments described', withFakeWebSocket(async () => {
    const queued = vi.spyOn(forebrainApi, 'runQueuedInput').mockResolvedValue({
      accepted: true, message: 'look at this', attachments: ['file-1'], mentionImages: ['shots/a.png'], preview: emptyPreview,
    })
    const info = vi.spyOn(forebrainApi, 'fileInfo').mockResolvedValue({ id: 'file-1', originalName: 'spec.pdf', mediaType: 'application/pdf' })
    try {
      const { stream, socket, pendingSend } = await startLiveStream('live-recall', 'run-recall')

      await expect(stream.editLastQueuedMessage()).resolves.toEqual({
        text: 'look at this',
        attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }],
        mentionImages: ['shots/a.png'],
      })
      expect(queued).toHaveBeenCalledWith('run-recall', { action: 'edit_last' })

      endTurn(socket, 'evt-done', 1, 'run-recall', 'live-recall')
      await pendingSend
    } finally {
      queued.mockRestore()
      info.mockRestore()
    }
  }))

  it('recalls nothing, and does not throw, when the run ended before the key', withFakeWebSocket(async () => {
    const queued = vi.spyOn(forebrainApi, 'runQueuedInput').mockRejectedValue(new Error('active run input not available'))
    try {
      const { stream, socket, pendingSend } = await startLiveStream('live-gone', 'run-gone')
      await expect(stream.editLastQueuedMessage()).resolves.toBeNull()
      endTurn(socket, 'evt-done', 1, 'run-gone', 'live-gone')
      await pendingSend
    } finally {
      queued.mockRestore()
    }
  }))

  // Queue semantics characterization (SUBAGENT_CONVERSATION plan 004, step 1).
  // The web entries of Q1–Q12 the browser actually owns are pinned by these
  // tests plus the existing ones named below; the entries the web does not
  // have (the queue itself lives behind the gateway) are not duplicated:
  //
  //   Q1  attachments cannot steer, they queue .... 'queues a message sent during a run whole, and sends it whole when the run hands it back'
  //   Q4  recall asks the run, hands back whole ... 'recalls the newest queued message whole, with its attachments described',
  //                                                'recalls a held message before its run exists'
  //   Q5  a normally-ended run hands back sends ... 'queues a message sent during a run whole, and sends it whole when the run hands it back',
  //                                                'follows a run resumed after an approval to its end, then sends what it handed back',
  //                                                'sends what a new conversation's first run hands back into that conversation'
  //   Q7  a failed run's messages come back ........ 'gives back what a failed run hands back instead of sending it',
  //                                                'returns what a run hands back to the composer when its conversation was left',
  //                                                'gives a held message back after the withdrawn send it was waiting behind'
  it('Q1: steers a text-only message sent during a run', withFakeWebSocket(async () => {
    const steered = vi.spyOn(forebrainApi, 'runInput').mockResolvedValue({ accepted: true, preview: { ...emptyPreview, pendingSteers: ['also this'] } })
    try {
      const { stream, socket, pendingSend } = await startLiveStream('live-steer', 'run-steer')

      await stream.send('also this', { activeInputDisposition: 'steer' })
      expect(steered).toHaveBeenCalledWith('run-steer', { message: 'also this' })
      expect(stream.pendingInputPreview.value.pendingSteers).toEqual(['also this'])

      endTurn(socket, 'evt-done', 1, 'run-steer', 'live-steer')
      await pendingSend
    } finally {
      steered.mockRestore()
    }
  }))

})

describe('conversation timeline copy', () => {
  it('has both locales for every new visible string', () => {
    for (const locale of ['zh', 'en'] as const) {
      setLocale(locale)
      for (const key of ['chat.toolCanceled'] as const) {
        expect(t(key).length).toBeGreaterThan(0)
        expect(t(key)).not.toBe(key)
      }
    }
    setLocale('en')
  })
})

describe('approval anchoring on the conversation timeline', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  /**
   * A gate belongs directly above the call it gated - the same place the
   * terminal prints the line. Records name the tool step they hold, so the
   * API's own projection rule (host = assistant row for the run) does not
   * decide where they land.
   */
  it('lands a replayed approval directly above the call it gated', async () => {
    const issuedCall = (id: string, name: string) => JSON.stringify([
      { type: 'tool_calls', tool_calls: [{ id, type: 'function', function: { name, arguments: '{}' } }] },
    ])
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'a1', rowId: 1, role: 'assistant', content: '', partsJson: issuedCall('call-exit', 'exit_plan_mode') },
      { id: 'a2', rowId: 2, role: 'assistant', content: '', partsJson: issuedCall('call-shell', 'shell') },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'anchor-session', nextCursor: 4, highWater: 4, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'evt-1', sequence: 1, schemaVersion: 1, runId: 'run-1', sessionId: 'anchor-session', type: 'approval_requested', payload: { actionId: 'act-exit', actionKind: 'exit_plan_mode', toolStepId: 'call-exit' } },
        { id: 'evt-2', sequence: 2, schemaVersion: 1, runId: 'run-1', sessionId: 'anchor-session', type: 'approval_resolved', payload: { actionId: 'act-exit', actionKind: 'exit_plan_mode', decision: 'cancelled', toolStepId: 'call-exit', confirmation: '✗ You canceled forebrain\'s request to exit plan mode' } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'anchor-session', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'anchor-session'
      await stream.loadMessages('anchor-session')

      const turn = stream.messages.value[0]
      expect(turn?.blocks?.map((block) => block.kind)).toEqual(['approval', 'tool'])
      expect(turn?.blocks?.[0]).toMatchObject({
        kind: 'approval', actionId: 'act-exit', status: 'cancelled',
        confirmation: '✗ You canceled forebrain\'s request to exit plan mode',
      })
      // The gate's own card is withheld on every surface; the record lands
      // above the turn's remaining call, where the terminal prints the line.
      expect(turn?.blocks?.[1]).toMatchObject({ kind: 'tool', step: { stepId: 'call-shell' } })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('keeps a gate whose call this client cannot place on its run projection', async () => {
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 'floating-session', nextCursor: 1, highWater: 1, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'evt-1', sequence: 1, schemaVersion: 1, runId: 'run-9', sessionId: 'floating-session', type: 'approval_resolved', payload: { actionId: 'act-9', actionKind: 'shell', decision: 'cancelled', toolStepId: 'call-missing', confirmation: '✗ You canceled the request to run ls' } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 'floating-session', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 'floating-session'
      await stream.loadMessages('floating-session')

      const blocks = stream.messages.value.flatMap((message) => message.blocks ?? [])
      expect(blocks).toHaveLength(1)
      expect(blocks[0]).toMatchObject({ kind: 'approval', actionId: 'act-9', status: 'cancelled' })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })
})

/**
 * A conversation stopped by a usage limit waits for the runtime to continue it.
 * The wait is live state: the observer's binding says what is pending, events
 * after the binding change it, and the replay that overlaps the history rows
 * changes nothing — it may describe a wait a restarted gateway no longer has.
 */
describe('auto-continue after a usage limit', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  function observerEvent(socket: FakeChatWebSocket, id: string, sequence: number, type: string, payload: Record<string, unknown>) {
    socket.message({
      op: 'run_event',
      data: { id, sequence, type, run_id: 'stopped-run', session_id: 's1', created_at: '2026-09-29T23:00:00Z', payload },
    })
  }

  it('shows the wait the binding reports, follows live events, and lets the page cancel it', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    const spies = [
      vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([]),
      vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' }),
      vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
        sessionId: 's1', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
      } as never),
      vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's1', records: [] }),
    ]
    const cancelSpy = vi.spyOn(forebrainApi, 'cancelAutoContinue').mockResolvedValue({ cancelled: true })
    try {
      const stream = useChatStream()
      stream.switchToSession('s1')
      await vi.waitFor(() => expect(stream.historyLoading.value).toBe(false))
      const observer = FakeChatWebSocket.instances[0]!
      observer.open()
      expect(JSON.parse(observer.sent[0] ?? '{}')).toMatchObject({ op: 'bind_session', session_id: 's1' })

      observer.message({
        op: 'session_bound',
        session_id: 's1',
        data: { cursor: 0, high_water: 5, auto_continue: { continue_at: '2026-09-30T02:10:05Z', code: 'rate_limit_quota', attempt: 1 } },
      })
      expect(stream.autoContinue.value).toMatchObject({ continueAt: '2026-09-30T02:10:05Z', attempt: 1 })

      // The replay up to the binding's high water is already accounted for.
      observerEvent(observer, 'evt-replayed-cancel', 4, 'auto_continue_cancelled', { reason: 'user' })
      observerEvent(observer, 'evt-replayed-start', 5, 'auto_continue_started', { prompt: 'old continuation' })
      expect(stream.autoContinue.value).not.toBeNull()
      expect(stream.messages.value).toEqual([])

      // A new turn superseded it, then the runtime armed the next attempt.
      observerEvent(observer, 'evt-superseded', 6, 'auto_continue_cancelled', { reason: 'superseded' })
      expect(stream.autoContinue.value).toBeNull()
      observerEvent(observer, 'evt-scheduled', 7, 'auto_continue_scheduled', { continue_at: '2026-09-30T07:10:05Z', code: 'rate_limit_quota', attempt: 2 })
      expect(stream.autoContinue.value).toMatchObject({ continueAt: '2026-09-30T07:10:05Z', attempt: 2 })
      // Events are filed under the stopped run; they must not reopen its turn.
      expect(stream.messages.value).toEqual([])

      expect(stream.cancelAutoContinue()).toBe(true)
      expect(stream.autoContinue.value).toBeNull()
      expect(cancelSpy).toHaveBeenCalledWith('s1')
      expect(stream.cancelAutoContinue()).toBe(false)

      // The wait ran out on another page's watch: the continuation's message
      // appears here too, as the user row a reload will show.
      observerEvent(observer, 'evt-scheduled-2', 8, 'auto_continue_scheduled', { continue_at: '2026-09-30T08:10:05Z', code: 'rate_limit_quota', attempt: 1 })
      observerEvent(observer, 'evt-started', 9, 'auto_continue_started', { attempt: 1, prompt: 'Continue from where you left off.' })
      expect(stream.autoContinue.value).toBeNull()
      expect(stream.messages.value.map((m) => [m.role, m.content])).toEqual([['user', 'Continue from where you left off.']])
      // The same event arriving again draws nothing more.
      observerEvent(observer, 'evt-started', 9, 'auto_continue_started', { attempt: 1, prompt: 'Continue from where you left off.' })
      expect(stream.messages.value).toHaveLength(1)
    } finally {
      for (const spy of spies) spy.mockRestore()
      cancelSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
  })

  it('keeps each agent’s wait in its own view and cancels it by its agent', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    const spies = [
      vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([]),
      vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' }),
      vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
        sessionId: 's1', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
      } as never),
      vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's1', records: [] }),
    ]
    const cancelSpy = vi.spyOn(forebrainApi, 'cancelAutoContinue').mockResolvedValue({ cancelled: true })
    try {
      const stream = useChatStream()
      stream.switchToSession('s1')
      await vi.waitFor(() => expect(stream.historyLoading.value).toBe(false))
      const observer = FakeChatWebSocket.instances[0]!
      observer.open()

      // The binding restores the conversation's wait and each subagent's.
      observer.message({
        op: 'session_bound',
        session_id: 's1',
        data: { cursor: 0, high_water: 3, auto_continues: [
          { continue_at: '2026-09-30T02:10:05Z', code: 'rate_limit_quota', attempt: 1 },
          { agent_id: 'task-a', continue_at: '2026-09-30T03:10:05Z', code: 'rate_limit_quota', attempt: 2 },
        ] },
      })
      expect(stream.autoContinue.value).toMatchObject({ continueAt: '2026-09-30T02:10:05Z', attempt: 1 })
      expect(stream.autoContinueForView('task-a')).toMatchObject({ continueAt: '2026-09-30T03:10:05Z', attempt: 2 })

      // A live event for another subagent lands in that subagent's own view and
      // leaves the conversation's wait alone.
      observerEvent(observer, 'evt-sub', 4, 'auto_continue_scheduled', { agent_id: 'task-b', continue_at: '2026-09-30T04:10:05Z', code: 'rate_limit_quota', attempt: 1 })
      expect(stream.autoContinueForView('task-b')).toMatchObject({ continueAt: '2026-09-30T04:10:05Z' })
      expect(stream.autoContinue.value).toMatchObject({ continueAt: '2026-09-30T02:10:05Z' })

      // Cancelling a subagent names it; the conversation's wait is untouched.
      expect(stream.cancelAutoContinue('task-a')).toBe(true)
      expect(cancelSpy).toHaveBeenCalledWith('s1', 'task-a')
      expect(stream.autoContinueForView('task-a')).toBeNull()
      expect(stream.autoContinue.value).not.toBeNull()
      expect(stream.cancelAutoContinue('task-a')).toBe(false)
    } finally {
      for (const spy of spies) spy.mockRestore()
      cancelSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
  })

  // The send that hit the limit hears the scheduled event on its own socket,
  // which can beat the observer's binding to the page. The binding's snapshot
  // is older than that event and must not take the notice down again.
  it('keeps a wait the send socket reported before the observer bound', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 's1'
      const pendingSend = stream.send('summarize the repo')
      const sendSocket = FakeChatWebSocket.instances[0]!
      sendSocket.open()
      sendSocket.message({ op: 'session_bound', request_id: 'r', session_id: 's1' })
      sendSocket.message({ op: 'run_started', request_id: 'r', run_id: 'stopped-run', session_id: 's1' })
      observerEvent(sendSocket, 'evt-scheduled', 12, 'auto_continue_scheduled', { continue_at: '2026-09-30T02:10:05Z', code: 'rate_limit_quota', attempt: 1 })
      sendSocket.message({ op: 'run_error', request_id: 'r', run_id: 'stopped-run', session_id: 's1', error: 'Usage limit reached' })
      await pendingSend
      expect(stream.autoContinue.value).toMatchObject({ continueAt: '2026-09-30T02:10:05Z' })

      const observer = FakeChatWebSocket.instances.find((socket) => socket !== sendSocket)!
      observer.open()
      observer.message({ op: 'session_bound', session_id: 's1', data: { cursor: 0, high_water: 11 } })
      expect(stream.autoContinue.value).toMatchObject({ continueAt: '2026-09-30T02:10:05Z' })
    } finally {
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
  })

  it('cancels the wait when the page sends a message', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    const cancelSpy = vi.spyOn(forebrainApi, 'cancelAutoContinue').mockResolvedValue({ cancelled: true })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 's1'
      stream.autoContinue.value = { continueAt: '2026-09-30T02:10:05Z', code: 'rate_limit_quota' }
      void stream.send('actually, do this instead')
      expect(stream.autoContinue.value).toBeNull()
      expect(cancelSpy).toHaveBeenCalledWith('s1')
    } finally {
      cancelSpy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
  })
})

/**
 * A heartbeat starts a turn nobody on this page asked for: its prompt reaches
 * every page watching the conversation as that turn's first row, marked as the
 * heartbeat's the way a reload shows it. The replay that overlaps the history
 * rows draws nothing — those rows already hold the prompt as the user row the
 * runtime wrote.
 */
describe('a heartbeat turn in its conversation', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  function observerEvent(socket: FakeChatWebSocket, id: string, sequence: number, type: string, payload: Record<string, unknown>) {
    socket.message({
      op: 'run_event',
      data: { id, sequence, type, run_id: 'beat-run', session_id: 's1', created_at: '2026-10-03T23:00:00Z', payload },
    })
  }

  it('draws the heartbeat prompt only when the event is new', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    const spies = [
      vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([]),
      vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' }),
      vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
        sessionId: 's1', nextCursor: 0, highWater: 0, hasMore: false, schemaVersion: 1, events: [],
      } as never),
      vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's1', records: [] }),
    ]
    try {
      const stream = useChatStream()
      stream.switchToSession('s1')
      await vi.waitFor(() => expect(stream.historyLoading.value).toBe(false))
      const observer = FakeChatWebSocket.instances[0]!
      observer.open()
      expect(JSON.parse(observer.sent[0] ?? '{}')).toMatchObject({ op: 'bind_session', session_id: 's1' })

      observer.message({
        op: 'session_bound',
        session_id: 's1',
        data: { cursor: 0, high_water: 5 },
      })

      // The replay up to the binding's high water is already in the history
      // rows the page loaded; drawing it again would show the prompt twice.
      observerEvent(observer, 'evt-replayed', 4, 'heartbeat_fired', { prompt: 'anything new?' })
      expect(stream.messages.value).toEqual([])

      // A beat fires while this page watches: its prompt becomes the turn's
      // first row, marked as the heartbeat's.
      observerEvent(observer, 'evt-fired', 6, 'heartbeat_fired', { prompt: 'anything new?' })
      expect(stream.messages.value).toEqual([
        { id: 'heartbeat-evt-fired', role: 'user', content: 'anything new?', origin: 'heartbeat' },
      ])

      // The same event arriving again — the reconnect the observer's identity
      // check exists for — draws nothing more.
      observerEvent(observer, 'evt-fired', 6, 'heartbeat_fired', { prompt: 'anything new?' })
      expect(stream.messages.value).toHaveLength(1)
    } finally {
      for (const spy of spies) spy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
  })

  it('reads the origin back off a stored user row', () => {
    const conversation = conversationFromTranscript([
      { role: 'user', content: 'anything new?', origin: 'heartbeat' },
      { role: 'assistant', content: 'nothing yet' },
      { role: 'user', content: 'thanks' },
    ])
    expect(conversation[0]).toMatchObject({ role: 'user', content: 'anything new?', origin: 'heartbeat' })
    expect(conversation[2].origin).toBeUndefined()
  })
})

/**
 * A language-server recommendation is a live question to the user: the
 * offer shows once, history never reopens it, and a duplicate delivery —
 * the reconnect the observer's identity check exists for — does not reset
 * what is already on screen.
 */
describe('language-server recommendations', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  function observerEvent(socket: FakeChatWebSocket, id: string, sequence: number, type: string, payload: Record<string, unknown>) {
    socket.message({
      op: 'run_event',
      data: { id, sequence, type, run_id: '', session_id: 's1', created_at: '2026-09-29T23:00:00Z', payload },
    })
  }

  const recommendationPayload = {
    id: 'lsprec-9f1c2a7b',
    server_id: 'gopls',
    display_name: 'gopls',
    languages: ['Go'],
    trigger_extension: '.go',
    mode: 'enable',
    binary_path: '/usr/local/bin/gopls',
    version: 'v0.23.0',
  }

  it('shows the live offer, skips the replay below the binding, and ignores a duplicate', async () => {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    const spies = [
      vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([]),
      vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' }),
      vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
        sessionId: 's1', nextCursor: 0, highWater: 5, hasMore: false, schemaVersion: 1, events: [],
      } as never),
      vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's1', records: [] }),
    ]
    try {
      const stream = useChatStream()
      stream.switchToSession('s1')
      await vi.waitFor(() => expect(stream.historyLoading.value).toBe(false))
      const observer = FakeChatWebSocket.instances[0]!
      observer.open()
      observer.message({
        op: 'session_bound',
        session_id: 's1',
        data: { cursor: 0, high_water: 5 },
      })

      // The replay up to the binding's high water is history: no offer.
      observerEvent(observer, 'evt-replayed', 4, 'lsp_recommendation', recommendationPayload)
      expect(stream.lspRecommendation.value).toBeNull()

      // The event after the binding is the live offer.
      observerEvent(observer, 'evt-live', 6, 'lsp_recommendation', recommendationPayload)
      expect(stream.lspRecommendation.value).toMatchObject({
        id: 'lsprec-9f1c2a7b', serverId: 'gopls', mode: 'enable', triggerExtension: '.go',
      })

      // The same event id again — a reconnect — changes nothing.
      stream.lspRecommendation.value = null
      observerEvent(observer, 'evt-live', 7, 'lsp_recommendation', recommendationPayload)
      expect(stream.lspRecommendation.value).toBeNull()
    } finally {
      for (const spy of spies) spy.mockRestore()
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
  })
})

/**
 * The subagent_* call cards: one card per call, drawn from the engine's own
 * facts and the lifecycle bindings, the same semantics the terminal settled
 * in 013. These tests hold the view model — the pure function every surface
 * of the web reads — and the binding the live event stream maintains.
 */
describe('subagent_* call cards', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  /** One live websocket run feeding run events in wire (snake_case) form. */
  function liveStream() {
    FakeChatWebSocket.instances = []
    const originalWebSocket = globalThis.WebSocket
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    const stream = useChatStream()
    const pending = stream.send('dispatch the work')
    const socket = FakeChatWebSocket.instances[0]!
    socket.open()
    const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
    let clock = 0
    const runEvent = (type: string, payload: Record<string, unknown>, at = '2026-10-07T08:00:00.000Z') => {
      clock += 1
      socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: { id: `evt-${clock}`, sequence: clock, type, run_id: 'run-1', session_id: 's1', created_at: at, payload },
      })
    }
    const finish = async () => {
      socket.message({ op: 'run_completed', request_id: sent.request_id, text: 'done', data: { elapsed_ms: 10 } })
      await pending
      Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: originalWebSocket })
    }
    return { stream, runEvent, finish }
  }

  it('formats a task clock exactly as the terminal does', () => {
    // The branches of pkg/tui formatDuration, one value each.
    expect(formatToolDuration(0)).toBe('')
    expect(formatToolDuration(50)).toBe('<0.1s')
    expect(formatToolDuration(4200)).toBe('4.2s')
    expect(formatToolDuration(59_000)).toBe('59s')
    expect(formatToolDuration(84_3000)).toBe('14m3s')
    expect(formatToolDuration(100)).toBe('0.1s')
    expect(formatToolDuration(12_000)).toBe('12s')
    expect(formatToolDuration(60_000)).toBe('1m0s')
    expect(formatToolDuration(401_000)).toBe('6m41s')
  })

  it('binds a background send to its dispatching call and clocks it from its own spawn', async () => {
    const { stream, runEvent, finish } = liveStream()
    runEvent('tool_call_started', {
      step_id: 'call-send-1',
      tool_name: 'subagent_send',
      summary: 'send 计划001 Go车道实施',
      tool_meta: {
        tool_name: 'subagent_send', status: 'running', invocation: 'send 计划001 Go车道实施',
        subagent_call: { verb: 'send', tasks: [{ index: 0, title: '计划001 Go车道实施', agent_type: 'general-purpose', status: 'waiting' }] },
      },
    })
    runEvent('subagent_spawned', {
      agent_id: 'subagent-6e5c042d', agent_type: 'general-purpose', task_id: 'subagent-6e5c042d',
      title: '计划001 Go车道实施', task: 'the whole dispatch prompt', execution_id: 'exec-1',
      parent_run_id: 'run-1', parent_tool_call_id: 'call-send-1', task_index: 0,
    }, '2026-10-07T08:00:02.000Z')
    runEvent('tool_call_started', {
      step_id: 'call-inner-1', tool_name: 'shell', summary: 'running gofmt',
      tool_meta: { tool_name: 'shell', status: 'running', agent_id: 'subagent-6e5c042d', invocation: 'shell gofmt -l pkg cmd' },
    }, '2026-10-07T08:00:03.000Z')
    const primaryOf = () => stream.messages.value[stream.messages.value.length - 1]
    const toolBlockOf = () => (primaryOf()?.blocks ?? []).find((b) => b.kind === 'tool' && b.step.stepId === 'call-send-1')
    expect(toolBlockOf()).toBeTruthy()
    const live = stream.subagentCallTasks.get('call-send-1')
    expect(live).toBeTruthy()

    // Before the end arrived, the row is a clock still running: the label is
    // the terminal's working-elapsed form, read at `now`.
    const running = subagentCallView((toolBlockOf() as Extract<TimelineBlock, { kind: 'tool' }>).step, live, Date.parse('2026-10-07T08:00:05.000Z'))
    expect(running.hasLiveClock).toBe(true)
    expect(running.rows[0].clock).toEqual({ kind: 'running', startedAtMs: Date.parse('2026-10-07T08:00:02.000Z'), label: '3s' })

    runEvent('subagent_ended', {
      agent_id: 'subagent-6e5c042d', agent_type: 'general-purpose', task_id: 'subagent-6e5c042d',
      status: 'ok', execution_id: 'exec-1', parent_run_id: 'run-1',
      parent_tool_call_id: 'call-send-1', task_index: 0, finished_at_ms: Date.parse('2026-10-07T08:00:12.000Z'),
    }, '2026-10-07T08:00:12.000Z')
    runEvent('tool_call_completed', {
      step_id: 'call-send-1', tool_name: 'subagent_send', summary: 'send 计划001 Go车道实施',
      duration_seconds: 0.02,
      tool_meta: {
        tool_name: 'subagent_send', status: 'completed', invocation: 'send 计划001 Go车道实施',
        subagent_call: {
          verb: 'send',
          tasks: [{ index: 0, title: '计划001 Go车道实施', agent_type: 'general-purpose', status: 'running', execution_id: 'exec-1', started_at: 1_799_223_202 }],
        },
      },
    })

    const settled = subagentCallView((toolBlockOf() as Extract<TimelineBlock, { kind: 'tool' }>).step, live, Date.parse('2026-10-07T08:00:12.000Z'))
    // The header says what the call did; a single dispatched task carries its
    // elapsed on the row, so the header names no duration of its own.
    expect(settled.headerKey).toBe('subagentCard.send.done')
    expect(settled.headerCount).toMatchObject({ count: 1, type: 'general-purpose', one: true })
    expect(settled.headerDuration).toBeUndefined()
    expect(settled.rows).toHaveLength(1)
    const row = settled.rows[0]
    expect(row.title).toBe('计划001 Go车道实施')
    // The row ends with the subagent's own final elapsed, from its spawn to
    // the finished_at_ms its end carried — not the call's 20ms.
    expect(row.clock).toEqual({ kind: 'final', label: formatToolDuration(10_000) })
    expect(row.agentId).toBe('subagent-6e5c042d')
    expect(row.latestTool).toBe('shell gofmt -l pkg cmd')
    // No standalone tool-use tally anywhere on the card.
    expect(row.moreToolUses).toBeUndefined()

    // The old lifecycle cards are gone: the dispatching call's card is the
    // conversation's whole account of the subagent — the timeline holds no
    // extra block for the dispatch, and nothing else of it exists.
    expect((primaryOf()?.blocks ?? []).filter((b) => b.kind === 'subagent')).toHaveLength(0)
    await finish()
  })

  it('draws each query card as the snapshot its call took', () => {
    const stepOf = (call: SubagentCall, status = 'completed'): SubagentToolStep => ({
      stepId: `call-${call.verb}`, toolName: `subagent_${call.verb}`, summary: call.verb, status, subagentCall: call,
    })

    // A status that found its task running, its call already done.
    const statusView = subagentCallView({
      ...stepOf({
        verb: 'status',
        tasks: [{ index: 0, key: 'subagent-6e5c', title: '计划001 Go车道实施', agentType: 'general-purpose', status: 'running', executionId: 'exec-1', startedAt: 1_799_223_000 }],
      }),
      durationSeconds: 0.02,
    }, undefined, (1_799_223_000 + 192) * 1000)
    expect(statusView.headerKey).toBe('subagentCard.status.done')
    expect(statusView.headerCount).toMatchObject({ count: 1, type: 'general-purpose' })
    expect(statusView.headerDuration).toBe('<0.1s')
    expect(statusView.rows[0]).toMatchObject({ status: 'running', clock: { kind: 'running', startedAtMs: 1_799_223_000_000, label: '3m 12s' } })

    // A wait that timed out says so, and does not claim the agent ended.
    const waitView = subagentCallView(stepOf({
      verb: 'wait',
      tasks: [{ index: 0, key: 'subagent-6e5c', title: '计划001 Go车道实施', agentType: 'general-purpose', status: 'running', timedOut: true, executionId: 'exec-1', startedAt: 1_799_223_000 }],
    }, 'completed'), undefined, 1_799_223_300_000)
    expect(waitView.headerKey).toBe('subagentCard.wait.done')
    expect(waitView.rows[0].timedOut).toBe(true)
    expect(waitView.rows[0].stopRequested).toBe(false)

    // A close that asked a still-running agent to stop.
    const closeView = subagentCallView(stepOf({
      verb: 'close',
      tasks: [{ index: 0, key: 'subagent-6e5c', title: '计划001 Go车道实施', agentType: 'general-purpose', status: 'running', stopRequested: true, executionId: 'exec-1', startedAt: 1_799_223_000 }],
    }))
    expect(closeView.headerKey).toBe('subagentCard.close.done')
    expect(closeView.rows[0].stopRequested).toBe(true)

    // A continue is its own card, timed from this continuation.
    const continueView = subagentCallView(stepOf({
      verb: 'continue',
      tasks: [{ index: 0, key: 'subagent-6e5c', title: '计划001 Go车道实施', agentType: 'general-purpose', status: 'done', executionId: 'exec-2', startedAt: 1_799_223_010, finishedAt: 1_799_223_170 }],
    }))
    expect(continueView.headerKey).toBe('subagentCard.continue.done')
    expect(continueView.rows[0].clock).toEqual({ kind: 'final', label: formatToolDuration(160_000) })

    // An empty list is a card that says it listed nothing and shows the
    // shared empty line.
    const emptyListView = subagentCallView(stepOf({ verb: 'list', tasks: [] }))
    expect(emptyListView.headerKey).toBe('subagentCard.list.done')
    expect(emptyListView.headerCount).toMatchObject({ count: 0 })
    expect(emptyListView.rows).toHaveLength(0)
    expect(emptyListView.empty).toBe(true)

    // A list with records names each, with its type as the row's phrase.
    const listView = subagentCallView(stepOf({
      verb: 'list',
      tasks: [
        { index: 0, key: 'subagent-6e5c', title: '计划001 Go车道实施', agentType: 'general-purpose', status: 'done', executionId: 'exec-1', startedAt: 1_799_223_000, finishedAt: 1_799_224_000 },
        { index: 1, key: 'subagent-8c5c', title: '计划002 Web车道实施', agentType: 'general-purpose', status: 'running', executionId: 'exec-2', startedAt: 1_799_223_100 },
      ],
    }))
    expect(listView.rows.map((r) => [r.listType, r.status])).toEqual([
      ['general-purpose', 'done'],
      ['general-purpose', 'running'],
    ])
  })

  it('counts every outcome of a settled fanout, a skipped task included', () => {
    const view = subagentCallView({
      stepId: 'call-fanout-1', toolName: 'subagent_fanout', summary: 'run 3 tasks', status: 'completed',
      durationSeconds: 401,
      subagentCall: {
        verb: 'run',
        tasks: [
          { index: 0, title: '任务16 migrate 导入', agentType: 'general-purpose', status: 'done', executionId: 'e1', startedAt: 1_799_223_000, finishedAt: 1_799_223_401 },
          { index: 1, title: '任务17 扩展目录', agentType: 'general-purpose', status: 'skipped', error: 'skipped: empty prompt' },
        ],
      },
    })
    expect(view.headerKey).toBe('subagentCard.run.done')
    expect(view.headerCount).toMatchObject({ count: 2, type: 'general-purpose', one: false })
    // A multi-task batch carries the wall clock on its header.
    expect(view.headerDuration).toBe('6m41s')
    expect(view.breakdown).toEqual({ done: 1, failed: 0, cancelled: 0, skipped: 1 })
    expect(view.rows[1]).toMatchObject({ status: 'skipped', error: 'skipped: empty prompt' })
    // The skipped row never started, so it carries no clock.
    expect(view.rows[1].clock).toBeUndefined()
  })

  it('opens one card for a plan review and binds its reviewer without a dispatch call', async () => {
    const { stream, runEvent, finish } = liveStream()
    const before = stream.messages.value.length
    runEvent('plan_review_started', { action_id: 'act-1', review_id: 'rev-1', provider: 'zhipuai', model: 'glm-5.3-flash', label: 'GLM' })
    runEvent('subagent_spawned', {
      agent_id: 'plan-review-rev-1', agent_type: 'plan-reviewer', task_id: 'plan-review-rev-1',
      title: 'Plan review', task: 'review the plan', execution_id: 'rev-1',
      parent_run_id: 'run-1', parent_tool_call_id: 'rev-1', task_index: 0,
    })
    runEvent('subagent_ended', {
      agent_id: 'plan-review-rev-1', agent_type: 'plan-reviewer', task_id: 'plan-review-rev-1',
      status: 'ok', execution_id: 'rev-1', parent_tool_call_id: 'rev-1', task_index: 0, finished_at_ms: Date.parse('2026-10-07T08:00:05.000Z'),
    })
    runEvent('plan_reviewed', { action_id: 'act-1', review_id: 'rev-1', provider: 'zhipuai', model: 'glm-5.3-flash', outcome: 'done', text: 'approved' })

    // One block of its own in the host timeline — no new message, no raw id.
    expect(stream.messages.value).toHaveLength(before)
    const host = stream.messages.value[stream.messages.value.length - 1]
    const blocks = (host?.blocks ?? []).filter((b) => b.kind === 'subagent')
    expect(blocks).toHaveLength(1)
    const block = blocks[0] as Extract<TimelineBlock, { kind: 'subagent' }>
    const live = stream.subagentCallTasks.get(block.stepId)
    expect(live).toBeTruthy()
    const view = subagentCallView(block.step, live, Date.parse('2026-10-07T08:00:06.000Z'))
    expect(view.headerKey).toBe('subagentCard.review.done')
    expect(view.rows[0]).toMatchObject({ title: 'Plan review', agentId: 'plan-review-rev-1' })
    await finish()
  })

  it('keeps a nested dispatch inside the subagent that made it', async () => {
    const { stream, runEvent, finish } = liveStream()
    // The child, dispatched by the primary agent with no call of its own.
    runEvent('subagent_spawned', {
      agent_id: 'subagent-child', agent_type: 'general-purpose', task_id: 'subagent-child',
      title: '审计计划引用', task: 'audit the plan', execution_id: 'exec-child',
      parent_run_id: 'run-1', parent_tool_call_id: '', task_index: 0,
    })
    // The child dispatches a grandchild through subagent_send.
    runEvent('tool_call_started', {
      step_id: 'call-nested-1', tool_name: 'subagent_send', summary: 'send 查找所有调用点',
      tool_meta: {
        tool_name: 'subagent_send', status: 'running', agent_id: 'subagent-child', invocation: 'send 查找所有调用点',
        subagent_call: { verb: 'send', tasks: [{ index: 0, title: '查找所有调用点', agent_type: 'explore', status: 'waiting' }] },
      },
    })
    runEvent('subagent_spawned', {
      agent_id: 'subagent-grand', agent_type: 'explore', task_id: 'subagent-grand',
      title: '查找所有调用点', task: 'find the callers', execution_id: 'exec-grand',
      parent_run_id: 'exec-child', parent_tool_call_id: 'call-nested-1', task_index: 0,
    })
    runEvent('subagent_ended', {
      agent_id: 'subagent-grand', agent_type: 'explore', task_id: 'subagent-grand',
      status: 'ok', execution_id: 'exec-grand', parent_run_id: 'exec-child',
      parent_tool_call_id: 'call-nested-1', task_index: 0, finished_at_ms: Date.parse('2026-10-07T08:00:09.000Z'),
    })
    runEvent('tool_call_completed', {
      step_id: 'call-nested-1', tool_name: 'subagent_send', summary: 'send 查找所有调用点',
      tool_meta: {
        tool_name: 'subagent_send', status: 'completed', agent_id: 'subagent-child', invocation: 'send 查找所有调用点',
        subagent_call: { verb: 'send', tasks: [{ index: 0, title: '查找所有调用点', agent_type: 'explore', status: 'done' }] },
      },
    })

    const messageCount = stream.messages.value.length
    // The primary timeline gained only the child's own card; the grandchild's
    // dispatch lives in the child's transcript.
    const primary = stream.messages.value[messageCount - 1]
    expect((primary?.blocks ?? []).some((b) => (
      (b.kind === 'tool' && b.step.stepId === 'call-nested-1') ||
      (b.kind === 'subagent' && b.stepId === 'subagent-exec:exec-grand')
    ))).toBe(false)
    const child = stream.subagents.value.find((entry) => entry.agentId === 'subagent-child')
    const nested = (child?.blocks ?? []).find((b) => b.kind === 'tool' && b.step.stepId === 'call-nested-1')
    expect(nested).toBeTruthy()
    const live = stream.subagentCallTasks.get('call-nested-1')
    expect(live?.get(0)).toMatchObject({ agentId: 'subagent-grand', status: 'done' })
    // And a lifecycle whose parent run is the child lands there too, never
    // as an empty new message in the main conversation.
    runEvent('subagent_spawned', {
      agent_id: 'subagent-grand2', agent_type: 'explore', task_id: 'subagent-grand2',
      title: '第二轮查找', task: 'find more', execution_id: 'exec-grand2',
      parent_run_id: 'exec-child', parent_tool_call_id: '', task_index: 0,
    })
    expect(stream.messages.value).toHaveLength(messageCount)
    const childAfter = stream.subagents.value.find((entry) => entry.agentId === 'subagent-child')
    expect((childAfter?.blocks ?? []).some((b) => b.kind === 'subagent' && b.id === 'subagent-exec:exec-grand2')).toBe(true)
    await finish()
  })

  it('settles the query rows that reported an execution when its end replays after them', async () => {
    const record = (status: string) => ({
      agent_id: 'subagent-6e5c', agent_kind: 'typed', agent_type: 'general-purpose',
      task_id: 'subagent-6e5c', title: '计划001 Go车道实施', status,
      started_at: 1_700_000_000, execution_id: 'exec-1', run_id: 'exec-1',
    })
    const chatMessagesSpy = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([
      { id: 'user-1', role: 'user', content: 'check it', runId: 'run-1' },
      {
        id: 'assistant-1', role: 'assistant', content: '', runId: 'run-1',
        partsJson: JSON.stringify([{ type: 'tool_calls', tool_calls: [{ id: 'call-status-1', type: 'function', function: { name: 'subagent_status', arguments: JSON.stringify({ agent_id: 'subagent-6e5c' }) } }] }]),
      },
      {
        id: 'tool-1', role: 'tool', content: JSON.stringify(record('running')), toolStepId: 'call-status-1', runId: 'run-1',
        partsJson: JSON.stringify([{ tool_call_id: 'call-status-1', type: 'tool_result_meta' }]),
        subagentCall: { verb: 'status', tasks: [{ index: 0, key: 'subagent-6e5c', title: '计划001 Go车道实施', agentType: 'general-purpose', status: 'running', executionId: 'exec-1', startedAt: 1_700_000_000 }] },
      },
    ] as never)
    const sessionEventsSpy = vi.spyOn(forebrainApi, 'sessionEvents').mockResolvedValue({
      sessionId: 's1', nextCursor: 3, highWater: 3, hasMore: false, schemaVersion: 1,
      events: [
        { id: 'spawn-1', sequence: 1, schemaVersion: 1, sessionId: 's1', runId: 'run-1', type: 'subagent_spawned', createdAt: '2023-11-14T22:13:20Z', payload: { agentId: 'subagent-6e5c', agentType: 'general-purpose', taskId: 'subagent-6e5c', title: '计划001 Go车道实施', task: 'the prompt', parentRunId: 'run-1', parentToolCallId: 'call-dispatch-1', taskIndex: 0, executionId: 'exec-1' } },
        { id: 'end-1', sequence: 2, schemaVersion: 1, sessionId: 's1', runId: 'run-1', type: 'subagent_ended', createdAt: '2023-11-14T22:15:20Z', payload: { agentId: 'subagent-6e5c', agentType: 'general-purpose', taskId: 'subagent-6e5c', status: 'ok', parentRunId: 'run-1', parentToolCallId: 'call-dispatch-1', taskIndex: 0, executionId: 'exec-1', finishedAtMs: Date.parse('2023-11-14T22:15:20Z') } },
      ],
    } as never)
    const legacySpy = vi.spyOn(forebrainApi, 'sessionSubagentHistory').mockResolvedValue({ sessionId: 's1', records: [] })
    try {
      const stream = useChatStream()
      stream.sessionId.value = 's1'
      await stream.loadMessages('s1')
      const live = stream.subagentCallTasks.get('call-status-1')
      expect(live?.get(0)).toMatchObject({ status: 'done', finishedAtMs: Date.parse('2023-11-14T22:15:20Z') })
    } finally {
      chatMessagesSpy.mockRestore()
      sessionEventsSpy.mockRestore()
      legacySpy.mockRestore()
    }
  })

  it('reads a reloaded call card from the row\'s facts, not its stored display body', () => {
    const fanoutArgs = JSON.stringify({
      max_parallel: 2,
      tasks: [
        { title: '任务16 migrate 导入', prompt: 'run the migration', subagent_type: 'explore' },
        { title: '任务17 扩展目录', prompt: 'expand', subagent_type: 'explore' },
      ],
    })
    const rows = [
      { role: 'user', content: 'go' },
      {
        role: 'assistant', content: '',
        partsJson: JSON.stringify([{ type: 'tool_calls', tool_calls: [{ id: 'call-fanout-9', type: 'function', function: { name: 'subagent_fanout', arguments: fanoutArgs } }] }]),
      },
      {
        role: 'tool', content: '{"results":[{"index":0,"ok":true},{"index":1,"ok":true}]}', toolStepId: 'call-fanout-9',
        // The stored display body is the old JSON the card used to print.
        partsJson: JSON.stringify([
          { text: '{"results":[{"index":0,"ok":true},{"index":1,"ok":true}]}', type: 'text' },
          { tool_call_id: 'call-fanout-9', type: 'tool_result_meta' },
          { type: 'tool_display', body: 'output:\n```json\n{"results":[]}\n```', summary: 'run 2 tasks', tool_meta_json: '{"tool_name":"subagent_fanout","status":"completed"}' },
        ]),
        subagentCall: {
          verb: 'run',
          tasks: [
            { index: 0, title: '任务16 migrate 导入', agentType: 'explore', status: 'done' },
            { index: 1, title: '任务17 扩展目录', agentType: 'explore', status: 'done' },
          ],
        } as SubagentCall,
      },
    ] as unknown as Parameters<typeof conversationFromTranscript>[0]
    const conversation = conversationFromTranscript(rows)
    const toolBlock = conversation.flatMap((message) => message.blocks ?? [])
      .find((b) => b.kind === 'tool' && b.step.stepId === 'call-fanout-9') as Extract<TimelineBlock, { kind: 'tool' }> | undefined
    expect(toolBlock).toBeTruthy()
    expect(toolBlock?.step.subagentCall?.tasks.map((task) => [task.title, task.status])).toEqual([
      ['任务16 migrate 导入', 'done'],
      ['任务17 扩展目录', 'done'],
    ])
    // The old display body survives on the step, but the card never reads it.
    expect(toolBlock?.step.output).toContain('output:')
    const view = subagentCallView(toolBlock!.step, undefined, 0)
    expect(view.headerKey).toBe('subagentCard.run.done')
    expect(view.rows.map((r) => r.title)).toEqual(['任务16 migrate 导入', '任务17 扩展目录'])
  })
})

describe('a subagent talked to from its own view', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  /** Swaps in the fake websocket for one test; the returned fn restores it. */
  function installFakeSocket(): () => void {
    const original = globalThis.WebSocket
    FakeChatWebSocket.instances = []
    Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: FakeChatWebSocket })
    return () => Object.defineProperty(globalThis, 'WebSocket', { configurable: true, writable: true, value: original })
  }

  it('a message the user sends a subagent never enters the conversation send path', async () => {
    const restore = installFakeSocket()
    const chatMessages = vi.spyOn(forebrainApi, 'chatMessages').mockResolvedValue([])
    const sessionMode = vi.spyOn(forebrainApi, 'sessionMode').mockResolvedValue({ mode: 'agent', phase: '' })
    const input = vi.spyOn(forebrainApi, 'subagentInput').mockResolvedValue({ delivery: 'steered', preview: emptyPendingInputPreview() })
    try {
      const stream = useChatStream()
      stream.switchToSession('s1')
      await Promise.resolve()
      const socketsBefore = FakeChatWebSocket.instances.length
      await stream.sendToSubagent('task-1', { text: 'continue', attachments: [], mentionImages: [] })
      expect(input).toHaveBeenCalledWith('s1', 'task-1', expect.objectContaining({ message: 'continue', mode: 'steer' }))
      // No new request socket and no turn: the conversation was never involved.
      expect(FakeChatWebSocket.instances.length).toBe(socketsBefore)
      expect(stream.isStreaming.value).toBe(false)
      expect(stream.messages.value).toEqual([])
    } finally {
      input.mockRestore(); sessionMode.mockRestore(); chatMessages.mockRestore(); restore()
    }
  })

  it('draws a user-sent message as the user block, live and via the same projection', async () => {
    const restore = installFakeSocket()
    try {
      const stream = useChatStream()
      const pending = stream.send('kick off')
      const socket = FakeChatWebSocket.instances[0]
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      const runEvent = (type: string, payload: Record<string, unknown>) => socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: { type, run_id: 'run-1', session_id: 's1', created_at: '2026-06-14T00:00:00.000Z', payload },
      })
      // A user-driven execution: its prompt is the user's own message.
      runEvent('subagent_spawned', {
        agent_id: 'task-1', agent_type: 'general-purpose', task_id: 'task-1',
        task: 'continue', execution_id: 'exec-1', origin: 'user',
      })
      // A steer delivered to a running subagent is also the user's message.
      runEvent('subagent_input_delivered', { agent_id: 'task-1', execution_id: 'exec-1', text: 'keep going' })
      // A dispatch-origin execution, for contrast, keeps the prompt block.
      runEvent('subagent_spawned', {
        agent_id: 'task-2', agent_type: 'explore', task_id: 'task-2',
        task: 'find the callers', execution_id: 'exec-2',
      })

      const userEntry = stream.subagents.value.find((row) => row.agentId === 'task-1')
      const userBlocks = (userEntry?.blocks ?? []).filter((block) => block.kind === 'user')
      expect(userBlocks.map((block) => (block as Extract<TimelineBlock, { kind: 'user' }>).text)).toEqual(['continue', 'keep going'])
      const promptEntry = stream.subagents.value.find((row) => row.agentId === 'task-2')
      expect((promptEntry?.blocks ?? []).some((block) => block.kind === 'prompt')).toBe(true)
      expect((promptEntry?.blocks ?? []).some((block) => block.kind === 'user')).toBe(false)

      socket.message({ op: 'run_completed', request_id: sent.request_id, text: '', data: { elapsed_ms: 1 } })
      await pending
    } finally {
      restore()
    }
  })

  it('shows a plan-reviewer its own budget from its own event, not the conversation’s', async () => {
    const restore = installFakeSocket()
    try {
      const stream = useChatStream()
      const pending = stream.send('review the plan')
      const socket = FakeChatWebSocket.instances[0]
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      const runEvent = (type: string, payload: Record<string, unknown>) => socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: { type, run_id: 'run-1', session_id: 's1', created_at: '2026-06-14T00:00:00.000Z', payload },
      })
      runEvent('subagent_spawned', {
        agent_id: 'plan-reviewer-1', agent_type: 'plan-reviewer', task_id: 'plan-reviewer-1',
        task: 'review the plan', execution_id: 'exec-r',
        model_provider: 'zhipuai', model: 'glm-5.3-flash',
      })
      runEvent('token_budget_updated', {
        agent_id: 'plan-reviewer-1', percent_left: 35, context_window: 1_000_000, token_usage: 650_000,
      })
      const entry = stream.subagents.value.find((row) => row.agentId === 'plan-reviewer-1')
      expect(entry?.agentType).toBe('plan-reviewer')
      expect(entry?.model).toBe('glm-5.3-flash')
      // The window is the reviewer's own model's, read from the event, never
      // the conversation's default.
      expect(entry?.tokenBudget).toMatchObject({ percentLeft: 35, contextWindow: 1_000_000 })
      socket.message({ op: 'run_completed', request_id: sent.request_id, text: '', data: { elapsed_ms: 1 } })
      await pending
    } finally {
      restore()
    }
  })

  it('a subagent boundary sends its next message and returns the rest to its own composer', async () => {
    const restore = installFakeSocket()
    const input = vi.spyOn(forebrainApi, 'subagentInput').mockResolvedValue({ delivery: 'steered', preview: emptyPendingInputPreview() })
    try {
      const stream = useChatStream()
      const pending = stream.send('kick off')
      const socket = FakeChatWebSocket.instances[0]
      socket.open()
      const sent = JSON.parse(socket.sent[0] ?? '{}') as Record<string, unknown>
      socket.message({ op: 'session_bound', request_id: sent.request_id, session_id: 's1' })
      socket.message({
        op: 'run_event',
        request_id: sent.request_id,
        data: {
          type: 'queued_input_released', run_id: 'run-1', session_id: 's1', created_at: '2026-06-14T00:00:00.000Z',
          payload: { agent_id: 'task-1', next: [{ text: 'go on' }], inputs: [{ text: 'hold this' }] },
        },
      })
      await vi.waitFor(() => {
        expect(input).toHaveBeenCalledWith('s1', 'task-1', expect.objectContaining({ message: 'go on' }))
      })
      await new Promise((resolve) => setTimeout(resolve, 0))
      expect(stream.takeSubagentReturnedDraft('task-1')).toMatchObject({ text: 'hold this' })
      socket.message({ op: 'run_completed', request_id: sent.request_id, text: '', data: { elapsed_ms: 1 } })
      await pending
    } finally {
      input.mockRestore(); restore()
    }
  })
})
