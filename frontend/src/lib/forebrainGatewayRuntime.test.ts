import { describe, expect, it } from 'vitest'

import sharedSubagentEvents from '../../../pkg/event/testdata/subagent_live_resume_events.json'

import {
  buildBrowserForebrainGatewayChatWsUrl,
  compactionFromHistoryRow,
  formatTokenCount,
  isSupportedForebrainRunEventSchema,
  parseCompactionEvent,
  parseForebrainMcpStatusMessage,
  parseForebrainRunEventMessage,
  parseForebrainSessionBoundMessage,
  parseForebrainSkillLifecycleNotification,
  parseForebrainTaskNotificationMessage,
  parseTurnDiffPayload,
} from './forebrainGatewayRuntime'

it('decodes the shared Go/TypeScript subagent live-resume fixture', () => {
  const events = sharedSubagentEvents.map((data) => parseForebrainRunEventMessage({ op: 'run_event', data }))
  expect(events.every(Boolean)).toBe(true)
  expect(events.map((event) => event?.sequence)).toEqual([41, 42, 43, 44, 45])
  expect(events[0]).toMatchObject({
    id: 'fixture-spawn-1',
    runId: 'child-run-1',
    sessionId: 'conversation-1',
    schemaVersion: 1,
    type: 'subagent_spawned',
    payload: {
      agentId: 'agent-1',
      workerSessionId: 'worker-1',
      parentToolCallId: 'fanout-call-1',
      taskIndex: 2,
      executionId: 'execution-1',
      futureField: 'ignored by older projectors',
    },
  })
  expect(events[3]).toMatchObject({
    type: 'approval_resolved',
    payload: { actionId: 'action-1', agentId: 'agent-1', decision: 'approved' },
  })
})

it('accepts legacy/v1 events and rejects explicitly unknown event schemas', () => {
  expect(isSupportedForebrainRunEventSchema(undefined)).toBe(true)
  expect(isSupportedForebrainRunEventSchema(1)).toBe(true)
  expect(isSupportedForebrainRunEventSchema(2)).toBe(false)
})

describe('forebrain gateway runtime', () => {
  it('parses canonical run_event payloads', () => {
    const evt = parseForebrainRunEventMessage({
      op: 'run_event',
      data: {
        id: 'evt-1',
        run_id: 'r1',
        session_id: 's1',
        type: 'assistant_delta',
        payload: { text: 'hello' },
        created_at: '2026-04-30T00:00:00Z',
      },
    })

    expect(evt).toEqual({
      id: 'evt-1',
      runId: 'r1',
      sessionId: 's1',
      type: 'assistant_delta',
      payload: { text: 'hello' },
      createdAt: '2026-04-30T00:00:00Z',
    })
  })

  it('parses compact and token budget canonical events', () => {
    const compacted = parseForebrainRunEventMessage({
      op: 'run_event',
      data: {
        id: 'evt-compact',
        run_id: 'r1',
        session_id: 's1',
        type: 'context_compacted',
        payload: {
          trigger: 'manual',
          strategy: 'remote_v2',
          reason: 'input budget exceeded',
          summary_source: 'remote_compaction',
          replaced_items: 12,
          summary: 'summarized prior turns',
          boundary_id: '42',
          tokens_before: 182000,
          tokens_after: 64000,
          reactive: true,
        },
        created_at: '2026-04-30T00:00:01Z',
      },
    })
    const budget = parseForebrainRunEventMessage({
      op: 'run_event',
      data: {
        id: 'evt-budget',
        run_id: 'r1',
        session_id: 's1',
        type: 'token_budget_updated',
        payload: {
          model: 'gpt-5',
          token_usage: 4096,
          percent_left: 98,
          context_window: 200000,
          effective_window: 180000,
          auto_compact_threshold: 167000,
        },
        created_at: '2026-04-30T00:00:02Z',
      },
    })

    expect(compacted?.type).toBe('context_compacted')
    expect(compacted?.payload).toMatchObject({
      trigger: 'manual',
      strategy: 'remote_v2',
      reason: 'input budget exceeded',
      replacedItems: 12,
      boundaryId: '42',
      tokensBefore: 182000,
      tokensAfter: 64000,
      reactive: true,
    })
    expect(budget?.type).toBe('token_budget_updated')
    expect(budget?.payload).toMatchObject({
      model: 'gpt-5',
      tokenUsage: 4096,
      percentLeft: 98,
      contextWindow: 200000,
      effectiveWindow: 180000,
      autoCompactThreshold: 167000,
    })
  })

  it('parses compact checkpoint metadata', () => {
    const evt = parseForebrainRunEventMessage({
      op: 'run_event',
      data: {
        id: 'evt-compact',
        run_id: 'r1',
        session_id: 's1',
        type: 'context_compacted',
        payload: {
          trigger: 'auto',
          strategy: 'remote_v2',
          summary_source: 'remote_compaction',
          scope: 'body_after_prefix',
          boundary_id: 'window-2',
          window_number: 2,
          tokens_before: 182000,
          tokens_after: 64000,
        },
        created_at: '2026-04-30T00:00:01Z',
      },
    })

    expect(evt?.payload).toMatchObject({
      trigger: 'auto',
      strategy: 'remote_v2',
      summarySource: 'remote_compaction',
      scope: 'body_after_prefix',
      boundaryId: 'window-2',
      windowNumber: 2,
      tokensBefore: 182000,
      tokensAfter: 64000,
    })
  })

  it('reads a compaction lifecycle as changes to one card', () => {
    const started = parseCompactionEvent('context_compacting', { compactionId: 'c1', trigger: 'auto', tokensBefore: 182400 })
    expect(started).toEqual({ compactionId: 'c1', agentId: undefined, patch: { status: 'running', percent: 0, trigger: 'auto', tokensBefore: 182400 } })
    const progress = parseCompactionEvent('context_compact_progress', { compactionId: 'c1', percent: 140, phase: 'summarizing', agentId: 'explorer' })
    expect(progress?.agentId).toBe('explorer')
    // A running compaction never claims to be complete.
    expect(progress?.patch).toEqual({ status: 'running', percent: 99, phase: 'summarizing' })
    const done = parseCompactionEvent('context_compacted', { compactionId: 'c1', tokensBefore: 182400, tokensAfter: 12300, duration: '14.2s', summary: 'checkpoint', strategy: 'local' })
    expect(done?.patch).toMatchObject({ status: 'done', percent: 100, tokensAfter: 12300, duration: '14.2s', summary: 'checkpoint' })
    expect(parseCompactionEvent('context_compact_failed', { compactionId: 'c2', cancelled: true })?.patch.status).toBe('cancelled')
    expect(parseCompactionEvent('context_compact_failed', { compactionId: 'c3', error: 'provider down' })?.patch).toMatchObject({ status: 'failed', error: 'provider down' })
    expect(parseCompactionEvent('context_compacted', { trigger: 'auto' })).toBeUndefined()
  })

  it('reads a finished compaction from its history row', () => {
    expect(compactionFromHistoryRow({ status: 'done', compactionId: 'c1', tokensBefore: 900, tokensAfter: 100 })).toMatchObject({ status: 'done', percent: 100, tokensAfter: 100 })
    expect(compactionFromHistoryRow({ status: 'running', compactionId: 'c1' })).toBeUndefined()
  })

  it('formats token counts like the terminal', () => {
    expect([355, 1000, 12250, 182400, 1250000].map(formatTokenCount)).toEqual(['355', '1k', '12.3k', '182.4k', '1.3M'])
  })

  it('parses pending input queue canonical events', () => {
    const evt = parseForebrainRunEventMessage({
      op: 'run_event',
      data: {
        id: 'evt-pending-input',
        run_id: 'r1',
        session_id: 's1',
        type: 'pending_input_updated',
        payload: {
          pending_steers: ['steer now'],
          rejected_steers: ['end of turn'],
          queued_messages: ['follow up'],
        },
        created_at: '2026-06-09T00:00:00Z',
      },
    })

    expect(evt).toEqual({
      id: 'evt-pending-input',
      runId: 'r1',
      sessionId: 's1',
      type: 'pending_input_updated',
      payload: {
        pendingSteers: ['steer now'],
        rejectedSteers: ['end of turn'],
        queuedMessages: ['follow up'],
      },
      createdAt: '2026-06-09T00:00:00Z',
    })
  })

  it('parses turn diff payloads (stats, backward-compat)', () => {
    expect(parseTurnDiffPayload({
      files: [{ path: '/tmp/demo.txt', status: 'modified', added: 1, deleted: 1 }],
      summary: '/tmp/demo.txt (+1/-1)',
    })).toEqual([
      {
        path: '/tmp/demo.txt',
        old_path: undefined,
        status: 'modified',
        added: 1,
        deleted: 1,
        binary: undefined,
        hunks: undefined,
        summary: '/tmp/demo.txt (+1/-1)',
      },
    ])
  })

  it('parses turn diff payloads with hunks', () => {
    const result = parseTurnDiffPayload({
      files: [{
        path: 'foo.go',
        added: 1,
        deleted: 1,
        hunks: [{
          old_start: 5,
          new_start: 5,
          lines: [
            { kind: 'ctx', old_no: 5, new_no: 5, text: 'ctx' },
            { kind: 'del', old_no: 6, text: 'old' },
            { kind: 'add', new_no: 6, text: 'new' },
          ],
        }],
      }],
    })
    expect(result[0].hunks).toHaveLength(1)
    expect(result[0].hunks![0].old_start).toBe(5)
    expect(result[0].hunks![0].lines[1].kind).toBe('del')
    // no "diff" string in output
    expect((result[0] as Record<string, unknown>)['diff']).toBeUndefined()
  })

  it('parses session_bound websocket messages', () => {
    expect(parseForebrainSessionBoundMessage({
      op: 'session_bound',
      request_id: 'req-1',
      session_id: 'default',
      message: 'bound',
    })).toEqual({
      requestId: 'req-1',
      sessionId: 'default',
      message: 'bound',
    })
  })

  it('parses session switch metadata from session_bound websocket messages', () => {
    expect(parseForebrainSessionBoundMessage({
      op: 'session_bound',
      request_id: 'req-1',
      session_id: 'fresh',
      message: 'session: switched',
      data: { session_switched: true },
    })).toEqual({
      requestId: 'req-1',
      sessionId: 'fresh',
      message: 'session: switched',
      sessionSwitched: true,
    })
  })

  it('parses task_notification payloads with camel-cased detail', () => {
    const evt = parseForebrainTaskNotificationMessage({
      op: 'task_notification',
      data: {
        event: 'progress',
        message: 'clone in progress',
        task: {
          id: 'task-1',
          kind: 'work_item',
          state: 'running',
          session_id: 'default',
          title: 'Skill lifecycle',
          progress: 0.45,
          result: JSON.stringify({
            phase: 'downloading',
            phase_label: '下载中',
            installed_names: ['alpha', 'beta'],
          }),
          created_at: '2026-05-17T00:00:00Z',
          updated_at: '2026-05-17T00:00:01Z',
        },
      },
    })

    expect(evt).toMatchObject({
      event: 'progress',
      message: 'clone in progress',
      task: {
        id: 'task-1',
        kind: 'work_item',
        state: 'running',
        sessionId: 'default',
        title: 'Skill lifecycle',
        progress: 0.45,
      },
      detail: {
        phase: 'downloading',
        phaseLabel: '下载中',
        installedNames: ['alpha', 'beta'],
      },
    })
  })

  it('parses skill lifecycle notifications with normalized progress', () => {
    const evt = parseForebrainSkillLifecycleNotification({
      op: 'task_notification',
      data: {
        event: 'done',
        message: 'installed: cc-skills-golang',
        task: {
          id: 'task-2',
          title: 'Skill lifecycle',
          result: JSON.stringify({
            phase: 'completed',
            phase_label: '完成',
            installed_names: ['cc-skills-golang'],
          }),
          created_at: '2026-05-17T00:00:02Z',
          updated_at: '2026-05-17T00:00:03Z',
        },
      },
    })

    expect(evt).toEqual({
      taskId: 'task-2',
      event: 'done',
      message: 'installed: cc-skills-golang',
      progress: 1,
      phase: 'completed',
      phaseLabel: '完成',
      installedNames: ['cc-skills-golang'],
      detail: {
        phase: 'completed',
        phaseLabel: '完成',
        installedNames: ['cc-skills-golang'],
      },
      createdAt: '2026-05-17T00:00:02Z',
      updatedAt: '2026-05-17T00:00:03Z',
    })
  })

  it('builds browser chat websocket urls without credentials in the url', () => {
    const originalWindow = globalThis.window
    Object.defineProperty(globalThis, 'window', {
      value: {
        location: {
          protocol: 'https:',
          host: 'example.com',
        },
      },
      configurable: true,
    })
    try {
      expect(buildBrowserForebrainGatewayChatWsUrl()).toBe('wss://example.com/ws/chat')
    } finally {
      Object.defineProperty(globalThis, 'window', {
        value: originalWindow,
        configurable: true,
      })
    }
  })
})

describe('parseForebrainMcpStatusMessage', () => {
  it('reads a snapshot in configuration order with the four states', () => {
    const status = parseForebrainMcpStatusMessage({
      op: 'mcp_status_event',
      session_id: 's-1',
      data: {
        generation: 'gen-1',
        pending: true,
        servers: [
          { name: 'docs', conn_status: 'connected', tool_count: 3, required: false },
          { name: 'slow', conn_status: 'connecting', required: true },
          { name: 'skip', conn_status: 'cancelled' },
          { name: 'broken', conn_status: 'error', error: 'spawn npx: executable file not found in $PATH' },
        ],
      },
    })

    expect(status?.generation).toBe('gen-1')
    expect(status?.pending).toBe(true)
    expect(status?.servers.map((s) => s.name)).toEqual(['docs', 'slow', 'skip', 'broken'])
    expect(status?.servers.map((s) => s.connStatus)).toEqual(['connected', 'connecting', 'cancelled', 'error'])
    expect(status?.servers[1].required).toBe(true)
    expect(status?.servers[3].error).toBe('spawn npx: executable file not found in $PATH')
    expect(status?.servers[0].toolCount).toBe(3)
  })

  it('ignores the inbound request op, so a reply is never read as a notification', () => {
    // mcp_status is the client's own request for this information; only
    // mcp_status_event is the server pushing it. Two ops with one name could not
    // be told apart by a client matching a reply to its request.
    expect(parseForebrainMcpStatusMessage({ op: 'mcp_status', data: { servers: [{ name: 'docs' }] } })).toBeNull()
  })

  it('drops entries with no name rather than rendering an anonymous row', () => {
    const status = parseForebrainMcpStatusMessage({
      op: 'mcp_status_event',
      data: { servers: [{ conn_status: 'error' }, { name: 'docs', conn_status: 'connected' }] },
    })
    expect(status?.servers.map((s) => s.name)).toEqual(['docs'])
  })

  it('keeps an unknown state as it arrived instead of mapping it onto a known one', () => {
    const status = parseForebrainMcpStatusMessage({
      op: 'mcp_status_event',
      data: { servers: [{ name: 'future', conn_status: 'throttled' }] },
    })
    expect(status?.servers[0].connStatus).toBe('throttled')
  })

  it('reports an empty subscription as an empty list, which is what a settled generation looks like', () => {
    const status = parseForebrainMcpStatusMessage({ op: 'mcp_status_event', data: { generation: 'gen-2', servers: [] } })
    expect(status?.servers).toEqual([])
    expect(status?.pending).toBe(false)
  })
})
