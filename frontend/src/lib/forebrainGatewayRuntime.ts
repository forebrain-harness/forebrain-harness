import { toCamelCase } from './case'
import { parseAutoContinue, parseAutoContinueEntries, type AutoContinueEntry, type AutoContinueState } from './autoContinue'

// Must match pkg/gateway/ws_protocol.go. New clients declare this on every
// mutating/subscription operation; servers still accept an omitted version as
// the legacy rollout path.
export const FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION = '2026-04-18'
export const FOREBRAIN_RUN_EVENT_SCHEMA_VERSION = 1

export type ForebrainGatewayServerHello = {
  protocolVersion?: string
}

export type ForebrainRunEventType =
  | 'turn_started'
  | 'turn_completed'
  | 'turn_cancelled'
  | 'turn_error'
  | 'assistant_delta'
  | 'reasoning_delta'
  | 'reasoning_done'
  | 'usage_delta'
  | 'subagent_spawned'
  | 'subagent_ended'
  | 'subagent_input_delivered'
  | 'input_delivered'
  | 'tool_call_started'
  | 'tool_output_delta'
  | 'tool_call_completed'
  | 'turn_diff_updated'
  | 'approval_requested'
  | 'approval_resolved'
  | 'mode_changed'
  | 'context_compacting'
  | 'context_compact_progress'
  | 'context_compacted'
  | 'context_compact_failed'
  | 'token_budget_updated'
  | 'plan_updated'
  | 'pending_input_updated'
  | 'queued_input_released'
  | 'goal_started'
  | 'goal_round_started'
  | 'goal_completed'
  | 'auto_continue_scheduled'
  | 'auto_continue_started'
  | 'auto_continue_cancelled'
  | 'heartbeat_fired'

export type ForebrainRunEvent = {
  id?: string
  sequence?: number
  schemaVersion?: number
  runId?: string
  sessionId?: string
  type: ForebrainRunEventType | string
  payload?: Record<string, unknown>
  createdAt?: string
}

/**
 * One server's startup state from an outbound mcp_status_event.
 *
 * The op is deliberately not `mcp_status`: that is the inbound request a client
 * sends to ask for the same information, and two ops with one name cannot be
 * told apart by a client matching a reply to its request.
 */
export type ForebrainMcpStatus = {
  generation?: string
  pending: boolean
  servers: Array<{
    name: string
    connStatus: string
    transport?: string
    error?: string
    toolCount?: number
    required?: boolean
    generation?: string
  }>
}

export type ForebrainSessionBoundMessage = {
  requestId?: string
  sessionId?: string
  message?: string
  sessionSwitched?: boolean
  /**
   * The last sequence the binding's replay covers. Events at or below it
   * happened before the page bound; what they said about live state (a
   * continuation waiting on a usage limit) is superseded by the snapshot below.
   */
  highWater?: number
  /** The continuation this session is waiting to run, when there is one. */
  autoContinue?: AutoContinueState
  /**
   * Every continuation waiting in the session: the conversation's own and each
   * of its subagents', so a page opened mid-wait shows each in its own view.
   */
  autoContinues?: AutoContinueEntry[]
}

export type ForebrainTaskNotificationTask = {
  id?: string
  kind?: string
  state?: string
  sessionId?: string
  channelId?: string
  title?: string
  prompt?: string
  result?: string
  error?: string
  progress?: number
  logPath?: string
  createdAt?: string
  updatedAt?: string
}

export type ForebrainTaskNotification = {
  event: string
  message?: string
  task: ForebrainTaskNotificationTask
  detail?: Record<string, unknown>
}

export type ForebrainSkillLifecycleNotification = {
  taskId: string
  event: string
  message: string
  progress: number
  phase: string
  phaseLabel: string
  installedNames: string[]
  detail: Record<string, unknown>
  createdAt?: string
  updatedAt?: string
}

export type DiffLineKind = 'add' | 'del' | 'ctx'

export type DiffLine = {
  kind: DiffLineKind
  old_no?: number
  new_no?: number
  text: string
}

export type DiffHunk = {
  old_start: number
  new_start: number
  lines: DiffLine[]
}

export type ForebrainTurnDiff = {
  path?: string
  old_path?: string
  status?: string
  added?: number
  deleted?: number
  binary?: boolean
  hunks?: DiffHunk[]
  summary?: string
}

/** How a compaction stands: running, or how it ended. */
export type ForebrainCompactionStatus = 'running' | 'done' | 'failed' | 'cancelled'

/**
 * One compaction as its card shows it. A live card is built from the
 * compaction's lifecycle events — started, progress, and exactly one of done or
 * failed — and a reloaded one from the history row the server wrote for it.
 */
export type ForebrainCompaction = {
  compactionId: string
  status: ForebrainCompactionStatus
  /** 0–100; only a finished compaction reaches 100. */
  percent: number
  /** What a running compaction is doing: "summarizing", then "saving". */
  phase?: string
  trigger?: string
  tokensBefore?: number
  tokensAfter?: number
  /** The compaction's own duration, e.g. "14.2s". */
  duration?: string
  /** The checkpoint summary the history was replaced with. */
  summary?: string
  strategy?: string
  reactive?: boolean
  error?: string
}

/** A compaction event, read as the change it makes to that compaction's card. */
export type ForebrainCompactionUpdate = {
  compactionId: string
  /** The subagent whose history was compacted; absent for the conversation's own. */
  agentId?: string
  patch: Partial<ForebrainCompaction>
}

export const FOREBRAIN_COMPACTION_EVENT_TYPES = new Set([
  'context_compacting',
  'context_compact_progress',
  'context_compacted',
  'context_compact_failed',
])

function optionalString(v: unknown): string | undefined {
  const text = typeof v === 'string' ? v.trim() : ''
  return text || undefined
}

/**
 * parseCompactionEvent reads one compaction lifecycle event. Every event of one
 * compaction carries its compaction id, which is the one card they all draw.
 */
export function parseCompactionEvent(
  type: string,
  payload: Record<string, unknown> | undefined,
): ForebrainCompactionUpdate | undefined {
  if (!payload) return undefined
  const compactionId = optionalString(payload.compactionId)
  if (!compactionId) return undefined
  const agentId = optionalString(payload.agentId)
  switch (type) {
    case 'context_compacting':
      return {
        compactionId,
        agentId,
        patch: { status: 'running', percent: 0, trigger: optionalString(payload.trigger), tokensBefore: asNumber(payload.tokensBefore) },
      }
    case 'context_compact_progress':
      return {
        compactionId,
        agentId,
        patch: { status: 'running', percent: Math.min(99, Math.max(0, asNumber(payload.percent) ?? 0)), phase: optionalString(payload.phase) },
      }
    case 'context_compacted':
      return { compactionId, agentId, patch: { ...finishedCompactionFields(payload), status: 'done', percent: 100 } }
    case 'context_compact_failed':
      return {
        compactionId,
        agentId,
        patch: {
          status: payload.cancelled === true ? 'cancelled' : 'failed',
          trigger: optionalString(payload.trigger),
          error: optionalString(payload.error),
        },
      }
    default:
      return undefined
  }
}

function finishedCompactionFields(payload: Record<string, unknown>): Partial<ForebrainCompaction> {
  return {
    trigger: optionalString(payload.trigger),
    tokensBefore: asNumber(payload.tokensBefore),
    tokensAfter: asNumber(payload.tokensAfter),
    duration: optionalString(payload.duration),
    summary: optionalString(payload.summary),
    strategy: optionalString(payload.strategy),
    reactive: payload.reactive === true ? true : undefined,
    error: optionalString(payload.error),
  }
}

/**
 * compactionFromHistoryRow reads the finished compaction a history row
 * carries. The server places each one where the live conversation drew it.
 */
export function compactionFromHistoryRow(record: Record<string, unknown> | null | undefined): ForebrainCompaction | undefined {
  if (!record) return undefined
  const compactionId = optionalString(record.compactionId)
  const status = optionalString(record.status)
  if (!compactionId || (status !== 'done' && status !== 'failed' && status !== 'cancelled')) return undefined
  return { ...finishedCompactionFields(record), compactionId, status, percent: status === 'done' ? 100 : 0 }
}

/** A /goal's lifecycle events. */
export const FOREBRAIN_GOAL_EVENT_TYPES = new Set(['goal_started', 'goal_round_started', 'goal_completed'])

/** The reserved subagent a /goal's check runs as. */
export const FOREBRAIN_GOAL_CHECK_AGENT_TYPE = 'goal-evaluator'

/**
 * One line of a /goal: how it opened, a continuation round, or how it ended.
 * checkAgentId names the check that decided a round or the end, whose view
 * the line opens.
 */
export type ForebrainGoalLine = {
  phase: 'started' | 'round' | 'completed'
  objective?: string
  round?: number
  why?: string
  /** How it ended: done, stuck, capped, interrupted or failed. */
  status?: string
  rounds?: number
  durationMs?: number
  checkAgentId?: string
}

/** parseGoalEvent reads a goal line from its lifecycle event. */
export function parseGoalEvent(type: string, payload: Record<string, unknown> | undefined): ForebrainGoalLine | undefined {
  if (!payload) return undefined
  switch (type) {
    case 'goal_started':
      return { phase: 'started', objective: optionalString(payload.objective) }
    case 'goal_round_started':
      return { phase: 'round', round: asNumber(payload.round), why: optionalString(payload.why), checkAgentId: optionalString(payload.checkAgentId) }
    case 'goal_completed':
      return {
        phase: 'completed',
        objective: optionalString(payload.objective),
        status: optionalString(payload.status),
        why: optionalString(payload.why),
        rounds: asNumber(payload.rounds),
        durationMs: asNumber(payload.durationMs),
        checkAgentId: optionalString(payload.checkAgentId),
      }
    default:
      return undefined
  }
}

/** goalFromHistoryRow reads the goal line a history row carries. */
export function goalFromHistoryRow(record: Record<string, unknown> | null | undefined): ForebrainGoalLine | undefined {
  if (!record) return undefined
  const phase = optionalString(record.phase)
  if (phase !== 'started' && phase !== 'round' && phase !== 'completed') return undefined
  return {
    phase,
    objective: optionalString(record.objective),
    round: asNumber(record.round),
    why: optionalString(record.why),
    status: optionalString(record.status),
    rounds: asNumber(record.rounds),
    durationMs: asNumber(record.durationMs),
    checkAgentId: optionalString(record.checkAgentId),
  }
}

/** Token counts the way people read them: 355, 12.3k, 1.2M. */
export function formatTokenCount(n: number | undefined): string {
  if (n == null || !Number.isFinite(n)) return ''
  const value = Math.max(0, n)
  // Halves round up, the way the terminal rounds them.
  const oneDecimal = (v: number) => (Math.round(v * 10) / 10).toFixed(1).replace(/\.0$/, '')
  if (value < 1000) return String(Math.round(value))
  if (value < 1_000_000) return `${oneDecimal(value / 1000)}k`
  return `${oneDecimal(value / 1_000_000)}M`
}

export type ForebrainTokenBudget = {
  /** Roster key of the agent whose context this budget measures; unset is the primary agent's. */
  agentId?: string
  model?: string
  tokenUsage?: number
  percentLeft?: number
  contextWindow?: number
  effectiveWindow?: number
  autoCompactThreshold?: number
}

function asRecord(v: unknown): Record<string, unknown> | null {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null
  return v as Record<string, unknown>
}

function asTrimmedString(v: unknown): string | undefined {
  const s = String(v ?? '').trim()
  return s || undefined
}

function asNumber(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined
}

function parseJSONRecord(raw: string | undefined): Record<string, unknown> | undefined {
  if (!raw?.trim()) return undefined
  try {
    const parsed = JSON.parse(raw)
    const record = asRecord(parsed)
    return record ? toCamelCase(record) as Record<string, unknown> : undefined
  } catch {
    return undefined
  }
}

function parseTaskNotificationTask(raw: unknown): ForebrainTaskNotificationTask | null {
  const task = asRecord(raw)
  if (!task) return null
  return {
    id: asTrimmedString(task.id),
    kind: asTrimmedString(task.kind),
    state: asTrimmedString(task.state),
    sessionId: asTrimmedString(task.session_id),
    channelId: asTrimmedString(task.channel_id),
    title: asTrimmedString(task.title),
    prompt: asTrimmedString(task.prompt),
    result: asTrimmedString(task.result),
    error: asTrimmedString(task.error),
    progress: asNumber(task.progress),
    logPath: asTrimmedString(task.log_path),
    createdAt: asTrimmedString(task.created_at),
    updatedAt: asTrimmedString(task.updated_at),
  }
}

export function isTerminalRunEventType(type: string): boolean {
  const normalized = type.trim().toLowerCase()
  return normalized === 'turn_completed' || normalized === 'turn_cancelled' || normalized === 'turn_error'
}

export function isSupportedForebrainRunEventSchema(version: number | undefined): boolean {
  // Events written before schema negotiation omitted the field and retain the
  // v1 shape. Explicit versions must match so a future payload is never
  // partially projected as if fields it requires did not exist.
  return version == null || version === FOREBRAIN_RUN_EVENT_SCHEMA_VERSION
}

export function parseForebrainRunEventMessage(raw: unknown): ForebrainRunEvent | null {
  const msg = asRecord(raw)
  if (!msg) return null
  const op = String(msg.op ?? '').trim().toLowerCase()
  if (op !== 'run_event') return null
  const data = asRecord(msg.data)
  if (!data) return null
  const type = String(data.type ?? '').trim()
  if (!type) return null
  const payload = asRecord(data.payload) ?? undefined
  return {
    id: asTrimmedString(data.id),
    sequence: asNumber(data.sequence),
    schemaVersion: asNumber(data.schema_version),
    runId: asTrimmedString(data.run_id),
    sessionId: asTrimmedString(data.session_id),
    type,
    payload: payload ? toCamelCase(payload) as Record<string, unknown> : undefined,
    createdAt: asTrimmedString(data.created_at),
  }
}

export function parseForebrainSessionBoundMessage(raw: unknown): ForebrainSessionBoundMessage | null {
  const msg = asRecord(raw)
  if (!msg) return null
  const op = String(msg.op ?? '').trim().toLowerCase()
  if (op !== 'session_bound') return null
  const data = asRecord(msg.data)
  const sessionSwitched = data?.session_switched === true || data?.sessionSwitched === true
  const highWater = asNumber(data?.high_water)
  const autoContinue = parseAutoContinue(data?.auto_continue)
  const autoContinues = parseAutoContinueEntries(data?.auto_continues)
  return {
    requestId: asTrimmedString(msg.request_id),
    sessionId: asTrimmedString(msg.session_id),
    message: asTrimmedString(msg.message),
    ...(sessionSwitched ? { sessionSwitched } : {}),
    ...(highWater !== undefined ? { highWater } : {}),
    ...(autoContinue ? { autoContinue } : {}),
    ...(autoContinues.length ? { autoContinues } : {}),
  }
}

/**
 * parseForebrainMcpStatusMessage reads one outbound mcp_status_event.
 *
 * A server with no name is dropped rather than rendered as an anonymous row: the
 * name is what a reader acts on, and the payload is the server's own record.
 */
export function parseForebrainMcpStatusMessage(raw: unknown): ForebrainMcpStatus | null {
  const msg = asRecord(raw)
  if (!msg) return null
  const op = String(msg.op ?? '').trim().toLowerCase()
  if (op !== 'mcp_status_event') return null
  const data = asRecord(msg.data)
  if (!data) return null
  const rows = Array.isArray(data.servers) ? data.servers : []
  const servers: ForebrainMcpStatus['servers'] = []
  for (const item of rows) {
    const row = asRecord(item)
    if (!row) continue
    const name = asTrimmedString(row.name)
    if (!name) continue
    servers.push({
      name,
      connStatus: String(row.conn_status ?? row.connStatus ?? 'unknown').trim(),
      transport: asTrimmedString(row.transport),
      error: asTrimmedString(row.error),
      toolCount: asNumber(row.tool_count ?? row.toolCount),
      required: row.required === true,
      generation: asTrimmedString(row.generation),
    })
  }
  return {
    generation: asTrimmedString(data.generation),
    pending: data.pending === true,
    servers,
  }
}

export function parseForebrainGatewayServerHello(raw: unknown): ForebrainGatewayServerHello | null {
  const msg = asRecord(raw)
  if (!msg || String(msg.op ?? '').trim().toLowerCase() !== 'connected') return null
  const data = asRecord(msg.data)
  return { protocolVersion: asTrimmedString(data?.protocol_version ?? data?.protocolVersion) }
}

export function parseForebrainTaskNotificationMessage(raw: unknown): ForebrainTaskNotification | null {
  const msg = asRecord(raw)
  if (!msg) return null
  const op = String(msg.op ?? '').trim().toLowerCase()
  if (op !== 'task_notification') return null
  const data = asRecord(msg.data)
  if (!data) return null
  const task = parseTaskNotificationTask(data.task)
  if (!task) return null
  return {
    event: String(data.event ?? '').trim(),
    message: asTrimmedString(data.message),
    task,
    detail: parseJSONRecord(task.result),
  }
}

export function parseForebrainSkillLifecycleNotification(raw: unknown): ForebrainSkillLifecycleNotification | null {
  const notification = parseForebrainTaskNotificationMessage(raw)
  if (!notification) return null
  if (notification.task.title !== 'Skill lifecycle') return null
  const detail = notification.detail ?? {}
  const installedNames = Array.isArray(detail.installedNames)
    ? detail.installedNames.map((item) => String(item).trim()).filter(Boolean)
    : []
  const progress = Math.max(
    0,
    Math.min(
      asNumber(detail.progress)
        ?? notification.task.progress
        ?? (notification.event === 'done' ? 1 : 0),
      1,
    ),
  )
  return {
    taskId: notification.task.id ?? '',
    event: notification.event,
    message: notification.message ?? '',
    progress,
    phase: String(detail.phase ?? '').trim(),
    phaseLabel: String(detail.phaseLabel ?? '').trim(),
    installedNames,
    detail,
    createdAt: notification.task.createdAt,
    updatedAt: notification.task.updatedAt,
  }
}

export function parseTurnDiffPayload(payload: Record<string, unknown> | undefined): ForebrainTurnDiff[] {
  if (!payload) return []
  const rawFiles = Array.isArray(payload.files) ? payload.files : []
  const summary = typeof payload.summary === 'string' ? payload.summary : undefined
  return rawFiles
    .map((item) => asRecord(item))
    .filter((item): item is Record<string, unknown> => Boolean(item))
    .map((item) => ({
      path: typeof item.path === 'string' ? item.path : undefined,
      old_path: typeof item.old_path === 'string' ? item.old_path : undefined,
      status: typeof item.status === 'string' ? item.status : undefined,
      added: typeof item.added === 'number' ? item.added : undefined,
      deleted: typeof item.deleted === 'number' ? item.deleted : undefined,
      binary: typeof item.binary === 'boolean' ? item.binary : undefined,
      hunks: parseHunks(item.hunks),
      summary,
    }))
}

function parseHunks(raw: unknown): DiffHunk[] | undefined {
  if (!Array.isArray(raw)) return undefined
  return raw
    .map((h) => asRecord(h))
    .filter((h): h is Record<string, unknown> => Boolean(h))
    .map((h) => ({
      old_start: typeof h.old_start === 'number' ? h.old_start : 0,
      new_start: typeof h.new_start === 'number' ? h.new_start : 0,
      lines: parseDiffLines(h.lines),
    }))
}

function parseDiffLines(raw: unknown): DiffLine[] {
  if (!Array.isArray(raw)) return []
  return raw
    .map((l) => asRecord(l))
    .filter((l): l is Record<string, unknown> => Boolean(l))
    .map((l) => ({
      kind: (['add', 'del', 'ctx'].includes(l.kind as string) ? l.kind : 'ctx') as DiffLineKind,
      old_no: typeof l.old_no === 'number' ? l.old_no : undefined,
      new_no: typeof l.new_no === 'number' ? l.new_no : undefined,
      text: typeof l.text === 'string' ? l.text : '',
    }))
}

export function parseTokenBudgetPayload(
  payload: Record<string, unknown> | undefined,
): ForebrainTokenBudget | undefined {
  if (!payload) return undefined
  const budget = {
    agentId: asTrimmedString(payload.agentId ?? payload.agent_id),
    model: typeof payload.model === 'string' ? payload.model : undefined,
    tokenUsage: asNumber(payload.tokenUsage ?? payload.token_usage),
    percentLeft: asNumber(payload.percentLeft ?? payload.percent_left),
    contextWindow: asNumber(payload.contextWindow ?? payload.context_window),
    effectiveWindow: asNumber(payload.effectiveWindow ?? payload.effective_window),
    autoCompactThreshold: asNumber(payload.autoCompactThreshold ?? payload.auto_compact_threshold),
  }
  return Object.values(budget).some((v) => v !== undefined) ? budget : undefined
}

/**
 * The chat WebSocket lives on the same host the page was served from and
 * authenticates with the session cookie, which the browser sends on the
 * handshake — no credentials belong in the URL.
 */
export function buildBrowserForebrainGatewayChatWsUrl(): string {
  if (typeof window === 'undefined') return 'ws://127.0.0.1:8080/ws/chat'
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return new URL(`${protocol}//${window.location.host}/ws/chat`).toString()
}
