import { computed, reactive, ref, shallowRef } from 'vue'
import { toCamelCase } from '@/lib/case'
import {
  formatProviderError,
  parseProviderErrorDetail,
  type ProviderErrorDetail,
} from '@/lib/providerError'

export interface PlanStep {
  stepId: string
  stepType?: string
  description: string
  dependencies?: string[]
  parameters?: { query?: string[] }
  estimatedTime?: number
}

export interface ExecutionPlan {
  planId: string
  question: string
  steps: PlanStep[]
  estimatedTotalTime?: number
}

export interface TurnDiffData {
  path?: string
  old_path?: string
  status?: string
  added?: number
  deleted?: number
  binary?: boolean
  hunks?: import('@/lib/forebrainGatewayRuntime').DiffHunk[]
  summary?: string
}

export interface TokenBudgetData {
  model?: string
  tokenUsage?: number
  percentLeft?: number
  contextWindow?: number
  effectiveWindow?: number
  autoCompactThreshold?: number
}

export interface PlanUpdateItem {
  id?: string
  content: string
  status: string
  active?: string
}

export interface PlanUpdateData {
  title: string
  explanation?: string
  completed?: number
  total?: number
  items: PlanUpdateItem[]
  /** The in-progress item's title, as the engine derived it once — the task a working line names. */
  active?: string
  /** Roster key of the subagent whose list this is; absent for the conversation's own plan. */
  agentId?: string
}

export interface PendingInputPreview {
  pendingSteers: string[]
  rejectedSteers: string[]
  queuedMessages: string[]
}

// A plan block is the plan itself. It once also carried the tool calls made
// while it ran, but nothing ever appended a block to the accumulator those
// fields were filled through, so they were always empty: a call is shown as a
// tool block on the message, the same way whether a plan is running or not.
export interface PlanBlock {
  plan: ExecutionPlan
}

export interface ChatMessage {
  id?: string
  role: 'user' | 'assistant' | 'notice'
  content: string
	 runId?: string
  /**
   * What this turn said and did, in the order it happened: its prose, its
   * thinking, the calls it made and the gates it was stopped at. A turn is one
   * message however many rows the runtime wrote for it, and this is the shape
   * both a live turn and a reloaded one produce.
   *
   * `content` keeps the prose on its own for the things that want the answer as
   * text - copying it, the last-answer ref - and the timeline is what renders.
   */
  blocks?: TimelineBlock[]
  runStartedAt?: string
  runFinishedAt?: string
  workedDurationMs?: number
  plan?: ExecutionPlan | null
  planBlocks?: PlanBlock[]
  /** What a user message attached, in the order it was attached. */
  attachments?: ChatAttachmentRecord[]
  /** Who wrote a user message on the person's behalf: 'heartbeat'. */
  origin?: string
  turnDiffs?: TurnDiffData[]
  memoryCitation?: import('@/lib/api').MemoryCitation
  tokenBudget?: TokenBudgetData
  planUpdates?: PlanUpdateData[]
  workedPlanDone?: number
  workedPlanTotal?: number
  workedPlanActive?: string
  /** A choice a slash command asked for, on its notice. */
  picker?: SlashPicker
  /** The value picked from it; a picked notice offers nothing more. */
  picked?: string
  /**
   * Tool steps this turn's transcript carried whose cards are withheld — the
   * exit-plan gate holds no card on any surface. An approval record naming
   * one of these anchors to this turn: the decision it carries is history
   * even though the wait it held drew nothing.
   */
  gateStepIds?: string[]
}

/**
 * A choice a slash command asks for — the engine's picker, the same one the
 * terminal shows: what to choose among, which one is in force, and the
 * command a pick goes back to.
 */
export interface SlashPicker {
  command: string
  title: string
  hint?: string
  items: { value: string; label: string; description?: string; current?: boolean }[]
}

/** A pick made in a SlashPicker, sent back with the command that offered it. */
export interface SlashChoice {
  command: string
  value: string
}

function slashPickerOf(raw: unknown): SlashPicker | undefined {
  if (!raw || typeof raw !== 'object') return undefined
  const p = raw as Record<string, unknown>
  const items = Array.isArray(p.items) ? p.items : []
  const command = String(p.command ?? '').trim()
  if (!command || items.length === 0) return undefined
  return {
    command,
    title: String(p.title ?? '').trim(),
    hint: String(p.hint ?? '').trim() || undefined,
    items: items.map((item) => {
      const i = (item ?? {}) as Record<string, unknown>
      return {
        value: String(i.value ?? ''),
        label: String(i.label ?? ''),
        description: String(i.description ?? '').trim() || undefined,
        current: i.current === true,
      }
    }),
  }
}

/**
 * One tool call a subagent made, including the provider-executed web search
 * that has no client-side call of its own. A skill step (`category: 'skill'`)
 * carries the same shape plus the structured skill identity, so the card can
 * label itself without ever inspecting arguments or output bodies.
 */
export interface SubagentToolStep {
  stepId: string
  toolName: string
  summary: string
  status: string
  output?: string
  error?: string
  retainAsHistory?: boolean
  category?: string
  skillName?: string
  skillPath?: string
  /**
   * A subagent_* call's card facts, as the engine derived them once. A call
   * that carries them draws the subagent card — its prompt, its result and
   * its stored display body stay out of the conversation.
   */
  subagentCall?: SubagentCall
  /** The call's own execution time in seconds, when it settled. */
  durationSeconds?: number
}


/**
 * One tool_calls part of a persisted assistant row. It is the only place a
 * call's name and arguments are recoverable on reload: a tool row stores just
 * the call id it answers.
 */
export interface StoredToolCall {
  id: string
  name: string
  arguments: string
}

/**
 * parseStoredToolCalls reads every tool call an assistant row issued.
 *
 * The stored parts are the canonical record: an assistant row that issued a call
 * the run never executed has no tool row, and this is what shows it was issued
 * at all.
 */
export function parseStoredToolCalls(partsJson?: string | null): StoredToolCall[] {
  const raw = String(partsJson ?? '').trim()
  if (!raw) return []
  let parts: unknown
  try {
    parts = JSON.parse(raw)
  } catch {
    return []
  }
  if (!Array.isArray(parts)) return []
  const out: StoredToolCall[] = []
  for (const part of parts) {
    if (!part || typeof part !== 'object') continue
    const record = part as Record<string, unknown>
    if (String(record.type ?? '') !== 'tool_calls') continue
    const calls = Array.isArray(record.tool_calls) ? record.tool_calls : []
    for (const call of calls) {
      if (!call || typeof call !== 'object') continue
      const entry = call as Record<string, unknown>
      const id = String(entry.id ?? '').trim()
      if (!id) continue
      const fn = (entry.function && typeof entry.function === 'object' ? entry.function : {}) as Record<string, unknown>
      out.push({ id, name: String(fn.name ?? '').trim(), arguments: String(fn.arguments ?? '').trim() })
    }
  }
  return out
}

/**
 * parseToolDisplayPart reads the formatted completion card the runtime stores
 * beside a tool result. Its body is what the terminal showed the user; the
 * model-facing JSON in the row's own content is not, and must never be rendered
 * as prose.
 */
/**
 * The body a plan-review gate's persisted denial carries when a delivered
 * review — never the user — closed it. It is the runtime's internal handoff
 * key: no surface renders it, and every replay projection that meets it drops
 * the row whole. Must stay byte-identical with the runtime's
 * tool.PlanReviewDeliveredDisplayKey.
 */
export const PLAN_REVIEW_DELIVERED_KEY = 'Plan review delivered — the planner is revising the plan.'

/**
 * isPlanReviewDeliveredRow reports whether a stored tool row is the delivery
 * drop key: such a row is internal handoff plumbing, so the timeline draws
 * nothing for it.
 */
function isPlanReviewDeliveredRow(row: { partsJson?: string | null }): boolean {
  return String(parseToolDisplayPart(row.partsJson).body ?? '').trim() === PLAN_REVIEW_DELIVERED_KEY
}

/**
 * isCanceledExitRow reports whether a stored tool row is the settled answer a
 * canceled exit-plan gate left behind. The gate's wait holds no card on any
 * surface, and the walk-away's only record is the approval confirmation line —
 * so like a delivered review's row, this one draws nothing whole.
 */
function isCanceledExitRow(row: { partsJson?: string | null }): boolean {
  const meta = toolMetaRecord(parseToolDisplayPart(row.partsJson).toolMetaJson)
  return isExitPlanGate(metaString(meta, 'tool_name')) && metaString(meta, 'status').toLowerCase() === 'canceled'
}

/**
 * parseToolDisplayPart reads the formatted completion card the runtime stores
 * beside a tool result. Its body is what the terminal showed the user; the
 * model-facing JSON in the row's own content is not, and must never be rendered
 * as prose.
 */
export function parseToolDisplayPart(partsJson?: string | null): { body?: string; summary?: string; toolMetaJson?: string } {
  const raw = String(partsJson ?? '').trim()
  if (!raw) return {}
  let parts: unknown
  try {
    parts = JSON.parse(raw)
  } catch {
    return {}
  }
  if (!Array.isArray(parts)) return {}
  for (const part of parts) {
    if (!part || typeof part !== 'object') continue
    const record = part as Record<string, unknown>
    if (String(record.type ?? '') !== 'tool_display') continue
    return {
      body: String(record.body ?? '').trim() || undefined,
      summary: String(record.summary ?? '').trim() || undefined,
      toolMetaJson: String(record.tool_meta_json ?? '').trim() || undefined,
    }
  }
  return {}
}

/**
 * toolMetaRecord reads a persisted tool meta document. Its keys are snake_case
 * inside the stored JSON (the API's camel-casing stops at the string boundary),
 * so both spellings are read rather than assuming the transport's.
 */
export function toolMetaRecord(raw?: string | null, fallback?: string | null): Record<string, unknown> {
  const source = String(raw ?? '').trim() || String(fallback ?? '').trim()
  if (!source) return {}
  try {
    const parsed = JSON.parse(source)
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

function metaString(meta: Record<string, unknown>, key: string): string {
  const camel = key.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase())
  return String(meta[key] ?? meta[camel] ?? '').trim()
}

/**
 * toolCallTarget names what a call was aimed at, from the arguments the model
 * supplied. It is one value, never the argument document: a plan-mode call
 * carries a whole plan under `plan`, which is not something to print at the user.
 */
export function toolCallTarget(toolName: string, argsJSON: string): string {
  const name = toolName.trim().toLowerCase()
  if (name === 'exit_plan_mode' || name === 'enter_plan_mode' || name === 'user_interaction' || name === 'request_permissions') {
    return ''
  }
  let args: Record<string, unknown> = {}
  try {
    const parsed = JSON.parse(String(argsJSON ?? '').trim() || '{}')
    if (parsed && typeof parsed === 'object') args = parsed as Record<string, unknown>
  } catch {
    return ''
  }
  const fieldsByTool: Record<string, string[]> = {
    shell: ['command'],
    read: ['file_path'],
    read_file: ['file_path'],
    write_file: ['file_path'],
    edit_file: ['file_path'],
    apply_patch: ['file_path'],
    web_fetch: ['url'],
    websearch: ['query'],
    web_search: ['query'],
    retrieve_output: ['query'],
  }
  for (const field of fieldsByTool[name] ?? []) {
    const value = String(args[field] ?? '').trim()
    if (value) return value
  }
  return ''
}

/**
 * skillCardTitle names a skill step's card from its structured identity alone:
 * what is loading while in flight, what loaded once it settled. It never reads
 * arguments or output bodies — an explicit invocation carries no arguments worth
 * printing, and "review-agent {"explicit":true}" is exactly the noise this card
 * exists to prevent. Undefined for a step that is not a skill load.
 */
export function skillCardTitle(step: SubagentToolStep): string | undefined {
  if (String(step.category ?? '').trim().toLowerCase() !== 'skill') return undefined
  const name = String(step.skillName ?? '').trim() || String(step.toolName ?? '').trim()
  const status = String(step.status ?? '').trim().toLowerCase()
  if (!status || status === 'running' || status === 'pending' || status === 'awaiting approval') {
    return `Loading skill ${name}`
  }
  return `Skill ${name}`
}

/**
 * canceledToolStep is the card for a call the transcript shows as issued but
 * never as run: a gate the user canceled, or a call an interruption stopped.
 * Its status is the same "canceled" the terminal stamps on a pending card when a
 * run aborts, so both surfaces agree the call never ran.
 */
export function canceledToolStep(call: StoredToolCall): SubagentToolStep {
  const target = toolCallTarget(call.name, call.arguments)
  const label = translate('chat.toolCanceled')
  return {
    stepId: call.id,
    toolName: call.name,
    summary: target ? `${label} · ${target}` : label,
    status: 'canceled',
  }
}

/**
 * subagentCallFromMeta reads a tool meta document's card facts. The live
 * payload arrives camelized; a stored meta string is snake_case — both
 * spellings are read, the way metaString reads a scalar.
 */
function subagentCallFromMeta(meta: Record<string, unknown>): SubagentCall | undefined {
  const raw = meta.subagentCall ?? meta.subagent_call
  if (!raw || typeof raw !== 'object') return undefined
  const call = raw as Record<string, unknown>
  const tasks = Array.isArray(call.tasks) ? call.tasks : []
  const facts = tasks.map((task, index) => {
    const t = (task && typeof task === 'object' ? task : {}) as Record<string, unknown>
    return {
      index: Number(t.index ?? index) || index,
      key: stringOrUndefined(t.key),
      title: stringOrUndefined(t.title),
      agentType: stringOrUndefined(t.agentType ?? t.agent_type),
      status: stringOrUndefined(t.status),
      error: stringOrUndefined(t.error),
      timedOut: t.timedOut === true || t.timed_out === true,
      stopRequested: t.stopRequested === true || t.stop_requested === true,
      executionId: stringOrUndefined(t.executionId ?? t.execution_id),
      startedAt: positiveNumberOrUndefined(t.startedAt ?? t.started_at),
      finishedAt: positiveNumberOrUndefined(t.finishedAt ?? t.finished_at),
    }
  })
  return { verb: String(call.verb ?? '').trim() || 'run', tasks: facts }
}

function stringOrUndefined(raw: unknown): string | undefined {
  const text = String(raw ?? '').trim()
  return text || undefined
}

function positiveNumberOrUndefined(raw: unknown): number | undefined {
  const value = Number(raw)
  return Number.isFinite(value) && value > 0 ? value : undefined
}

/** The first line of a prompt, as a ledger record's task name. */
function firstLine(text: string): string {
  return String(text ?? '').split('\n').map((line) => line.trim()).find(Boolean) ?? ''
}

/**
 * toolStepFromPayload builds one card from a live tool step event. Both the
 * conversation and a subagent read the same runtime payload, so they label the
 * same call the same way.
 */
export function toolStepFromPayload(
  payload: Record<string, unknown>,
  completed: boolean,
  fallbackStepId: string,
): SubagentToolStep {
  const meta = subagentStepMeta(payload)
  const durationSeconds = Number(payload.durationSeconds)
  return {
    stepId: String(payload.stepId ?? '').trim() || fallbackStepId,
    toolName: String(payload.toolName ?? '').trim(),
    summary: subagentStepSummary(payload),
    status: subagentStepStatus(payload, completed),
    output: completed ? String(payload.displayBody ?? '').trim() || undefined : undefined,
    error: String(payload.error ?? '').trim() || undefined,
    retainAsHistory: payload.retainAsHistory === true,
    category: metaString(meta, 'category') || undefined,
    skillName: metaString(meta, 'skill_name') || undefined,
    skillPath: metaString(meta, 'skill_path') || undefined,
    subagentCall: subagentCallFromMeta(meta),
    durationSeconds: Number.isFinite(durationSeconds) && durationSeconds > 0 ? durationSeconds : undefined,
  }
}

/**
 * isExitPlanGate reports whether a call is the exit-plan gate, whose wait
 * paints no card on any surface: its approval prompt is the whole wait.
 * Bound to the runtime's tool.ToolStepHoldsNoCard.
 */
function isExitPlanGate(toolName: string): boolean {
  return String(toolName ?? '').trim().toLowerCase() === 'exit_plan_mode'
}

/**
 * stepHoldsNoCard reports whether a tool step's card is withheld: the
 * exit-plan gate while the call runs, awaits the decision, or was abandoned to
 * that wait - waiting, abandoned and canceled hold no card. Only the settled
 * answers draw: the user's denial, a failure, the exit itself.
 */
function stepHoldsNoCard(step: { toolName: string; status: string }): boolean {
  return isExitPlanGate(step.toolName) && ['running', 'awaiting approval', 'canceled'].includes(String(step.status ?? '').trim().toLowerCase())
}

/**
 * toolStepFromRow rebuilds one card from a persisted tool row. A subagent_*
 * row carries its card facts as the gateway's own field — the stored display
 * part is what the surface once showed, and the subagent card never reads it.
 * When a row carries none, the card is left without a body rather than
 * falling back to the model-facing result, which was never meant to be read.
 */
export function toolStepFromRow(row: { toolStepId?: string | null; toolMetaJson?: string | null; partsJson?: string | null; subagentCall?: SubagentCall | null }, call?: StoredToolCall): SubagentToolStep {
  const display = parseToolDisplayPart(row.partsJson)
  const meta = { ...toolMetaRecord(row.toolMetaJson), ...toolMetaRecord(display.toolMetaJson) }
  const toolName = metaString(meta, 'tool_name') || String(call?.name ?? '').trim()
  const summary = String(display.summary ?? '').trim() || metaString(meta, 'invocation') || toolName
  const status = metaString(meta, 'status') || 'completed'
  return {
    stepId: String(row.toolStepId ?? '').trim() || String(call?.id ?? '').trim(),
    toolName,
    summary,
    status,
    output: display.body,
    category: metaString(meta, 'category') || undefined,
    skillName: metaString(meta, 'skill_name') || undefined,
    skillPath: metaString(meta, 'skill_path') || undefined,
    subagentCall: row.subagentCall ?? undefined,
  }
}

/**
 * conversationFromTranscript rebuilds the conversation from stored rows.
 *
 * A turn is one message, whatever number of rows the runtime wrote for it: the
 * agent's prose, its thinking, and the calls it made are blocks on one
 * timeline, in the order they happened. That is the shape a live turn builds as
 * it streams, and a reload that produced any other shape would show the user a
 * different conversation from the one they had just watched - the whole answer
 * in one bubble with the calls stacked above it, or a bubble per row with the
 * cards scattered between them.
 *
 * Two rows never become messages of their own. A tool row is the answer to a
 * call, so it is drawn as that call's card - as a bubble it printed the
 * model-facing JSON on screen as though the agent had said it. An assistant row
 * that only issued calls has no prose, so it contributes cards and no text.
 */
export function conversationFromTranscript(
  rows: ChatMessageRecord[],
  renderPlan: (plan: ExecutionPlan) => boolean = () => true,
): ChatMessage[] {
  const roleOf = (row: ChatMessageRecord) => String(row.role ?? '').trim().toLowerCase()
  const calls = new Map<string, StoredToolCall>()
  const toolRowByCall = new Map<string, ChatMessageRecord>()
  for (const row of rows) {
    if (roleOf(row) === 'assistant') {
      for (const call of parseStoredToolCalls(row.partsJson)) calls.set(call.id, call)
      continue
    }
    if (roleOf(row) !== 'tool') continue
    const stepId = String(row.toolStepId ?? '').trim()
    // The first row wins: a call id can be persisted twice by a replayed turn,
    // and the second copy is the same card, not another one.
    if (stepId && !toolRowByCall.has(stepId)) toolRowByCall.set(stepId, row)
  }

  const out: ChatMessage[] = []
  let turn: ChatMessage | null = null
  const drawn = new Set<string>()
  const openTurn = (row: ChatMessageRecord, index: number) => {
    if (turn) return turn
    turn = {
      id: row.id || (row.rowId ? `message-${row.rowId}` : `assistant-${index}`),
      role: 'assistant',
      content: '',
      blocks: [],
    }
    out.push(turn)
    return turn
  }
  const spoken: string[] = []
  const closeTurn = () => {
    if (!turn) return
    turn.content = spoken.join('\n\n')
    if (!turn.blocks?.length) delete turn.blocks
    spoken.length = 0
    turn = null
  }

  rows.forEach((row, index) => {
    const role = roleOf(row)
    if (role === 'user') {
      closeTurn()
      const attachments = row.attachments ?? []
      out.push({
        id: row.id || (row.rowId ? `message-${row.rowId}` : `user-${index}`),
        role: 'user',
        content: row.content ?? '',
        runId: String(row.runId ?? '').trim() || undefined,
        ...(attachments.length ? { attachments } : {}),
        ...(row.memoryCitation ? { memoryCitation: row.memoryCitation } : {}),
        ...(row.origin ? { origin: row.origin } : {}),
      })
      return
    }
    if (role === 'goal') {
      // The server places each of a goal's lines where the live conversation
      // drew it; all of them belong to the turn working toward the goal.
      const goal = goalFromHistoryRow(row.goal ?? undefined)
      if (!goal) return
      const current = openTurn(row, index)
      current.blocks = [...(current.blocks ?? []), { kind: 'goal', id: goalBlockId(goal, current.blocks?.length ?? 0), goal }]
      return
    }
    if (role === 'compaction') {
      // The server places each finished compaction where the live
      // conversation drew it, so it joins the turn it sits in: first in the
      // answer to the message it made room for, between the steps of a turn it
      // ran in the middle of, or on its own when the user asked for it.
      const compaction = compactionFromHistoryRow(row.compaction ?? undefined)
      if (!compaction) return
      // The one the user asked for is a turn of its own, as it was live.
      const ownTurn = compaction.trigger === 'manual'
      if (ownTurn) closeTurn()
      const current = openTurn(row, index)
      current.blocks = [...(current.blocks ?? []), { kind: 'compaction', id: compactionBlockId(compaction.compactionId), compaction }]
      if (ownTurn) closeTurn()
      return
    }
    if (role === 'worked') {
      // The line that closed a run, after the last row it wrote, whatever
      // ended it. A run that said nothing - one that failed at once - has no
      // turn of its own yet, and its line is that turn.
      const current = openTurn(row, index)
      applyRunWorkedLine(current, row)
      closeTurn()
      return
    }
    if (role === 'tool') {
      // Answered in call order beside the row that issued it. One that no
      // assistant row claims - a misattributed or repaired row - keeps its own
      // place rather than disappearing.
      const stepId = String(row.toolStepId ?? '').trim()
      if (!stepId || drawn.has(stepId)) return
      // A delivered review closed this gate, not the user: the row's body is
      // the delivery drop key, so the row draws nothing. A canceled gate's
      // settled row draws nothing either — the wait's only record is the
      // approval confirmation line.
      if (isPlanReviewDeliveredRow(row)) return
      if (isCanceledExitRow(row)) return
      const current = openTurn(row, index)
      drawn.add(stepId)
      current.blocks = [...(current.blocks ?? []), { kind: 'tool', step: toolStepFromRow(row, calls.get(stepId)) }]
      return
    }
    const current = openTurn(row, index)
    const text = String(row.content ?? '')
    if (role === 'reasoning') {
      if (text.trim()) {
        current.blocks = [...(current.blocks ?? []), { kind: 'thinking', text, open: false, startedAt: 0 }]
      }
      return
    }
    if (text.trim()) {
      spoken.push(text)
      current.blocks = [...(current.blocks ?? []), { kind: 'assistant', text, open: false }]
    }
    for (const call of parseStoredToolCalls(row.partsJson)) {
      if (drawn.has(call.id)) continue
      const answer = toolRowByCall.get(call.id)
      // A delivered review closed this gate: the answer row's body is the
      // delivery drop key, so the call draws nothing - the same whole-row
      // drop an unclaimed row takes above. A canceled gate's settled answer
      // drops the same way.
      if (answer && isPlanReviewDeliveredRow(answer)) continue
      if (answer && isCanceledExitRow(answer)) continue
      // The exit-plan gate holds no card while it waits, and none when a run
      // stopped before it resolved: only its settled answers draw. The step
      // still anchors this turn, because an approval record naming it is
      // history even though the wait drew nothing.
      if (!answer && isExitPlanGate(String(call.name ?? ''))) {
        current.gateStepIds = [...(current.gateStepIds ?? []), String(call.id ?? '').trim()].filter(Boolean)
        continue
      }
      drawn.add(call.id)
      current.blocks = [...(current.blocks ?? []), {
        kind: 'tool',
        // A call with no tool row never ran: the gate the user canceled, or a
        // call an interruption stopped. It is a card all the same, and a
        // canceled one - the same thing the terminal shows for it.
        step: answer ? toolStepFromRow(answer, call) : canceledToolStep(call),
      }]
    }
    applyTranscriptRowMetadata(current, row, renderPlan)
  })
  closeTurn()
  return out
}

/**
 * applyTranscriptRowMetadata folds one assistant row's per-turn metadata onto
 * the turn it belongs to. The runtime writes these on whichever row it finished
 * on, and the turn is one message, so the last row carrying a value owns it -
 * the same way a live turn ends up with the last values its run reported.
 */
function applyTranscriptRowMetadata(
  message: ChatMessage,
  row: ChatMessageRecord,
  renderPlan: (plan: ExecutionPlan) => boolean,
) {
  if (String(row.runId ?? '').trim()) message.runId = String(row.runId).trim()
  if (row.memoryCitation) message.memoryCitation = row.memoryCitation
  if (!row.planJson) return
  try {
    const planVal = toCamelCase(JSON.parse(row.planJson) as Record<string, unknown>) as unknown as ExecutionPlan
    if (!renderPlan(planVal)) return
    message.plan = planVal
    message.planBlocks = [{ plan: planVal }]
  } catch {
    //
  }
}

/**
 * applyRunWorkedLine closes a turn with the run's worked line from its history
 * row: the run's clock, and its checklist facts the same way the live run end
 * carried them.
 */
function applyRunWorkedLine(message: ChatMessage, row: ChatMessageRecord) {
  if (String(row.runId ?? '').trim()) message.runId = String(row.runId).trim()
  if (row.runStartedAt) message.runStartedAt = String(row.runStartedAt)
  if (row.runFinishedAt) message.runFinishedAt = String(row.runFinishedAt)
  if (row.workedDurationMs != null) message.workedDurationMs = Number(row.workedDurationMs)
  const planTotal = Number(row.planTotal)
  if (Number.isFinite(planTotal) && planTotal > 0) {
    const planDone = Number(row.planDone)
    message.workedPlanTotal = planTotal
    message.workedPlanDone = Number.isFinite(planDone) ? planDone : 0
    if (typeof row.planActive === 'string' && row.planActive.trim()) message.workedPlanActive = row.planActive
  }
}

/**
 * spokenText is a turn's prose on its own: its assistant blocks joined the way
 * separate responses read as one answer. It is what `content` carries for the
 * things that want the answer as text - copying it, the last-answer ref - while
 * the timeline is what renders.
 */
export function spokenText(blocks: TimelineBlock[]): string {
  return blocks.filter((block) => block.kind === 'assistant').map((block) => block.text).join('\n\n')
}

/**
 * withSpokenDelta puts streamed text on the turn's timeline and re-derives its
 * prose. Text after thinking closes the thinking block, the way the terminal
 * finalises reasoning before it prints the answer.
 */
export function withSpokenDelta(message: ChatMessage, text: string, eventId?: string, occurredAt?: unknown): ChatMessage {
  const blocks = appendStreamedText(
    closeStreamedBlocks(message.blocks ?? [], ['thinking'], occurredAt),
    'assistant',
    text,
    eventId,
    occurredAt,
  )
  return { ...message, blocks, content: spokenText(blocks) || NOTHING_SAID_YET }
}

/**
 * withSettledTurnText closes a turn's streamed blocks and reconciles them with
 * the answer the run reported, then re-derives its prose from what is left.
 */
export function withSettledTurnText(message: ChatMessage, finalText: string, occurredAt?: unknown): ChatMessage {
  const blocks = settleFinalAnswerBlock(
    closeStreamedBlocks(message.blocks ?? [], ['assistant', 'thinking'], occurredAt),
    finalText,
  )
  return { ...message, blocks, content: spokenText(blocks) || finalText || message.content }
}

/**
 * toolStepPending reports a card the runtime has not finished with: dispatched,
 * running, or held at a gate.
 */
function toolStepPending(step: SubagentToolStep): boolean {
  const status = String(step.status ?? '').trim().toLowerCase()
  return status === '' || status === 'running' || status === 'pending' || status === 'awaiting approval'
}

/**
 * cancelPendingToolBlocks marks every card the run never finished as canceled,
 * which is what the terminal stamps on them when a run aborts
 * (Renderer.FinalizePendingTools). Without it a stopped turn kept showing its
 * last call as still waiting, and a reload - where the same call has no result
 * row and is rebuilt as canceled - disagreed with what was on screen.
 */
export function cancelPendingToolBlocks(blocks: TimelineBlock[]): TimelineBlock[] {
  return blocks.map((block) => (
    block.kind === 'tool' && toolStepPending(block.step)
      ? { ...block, step: { ...block.step, status: 'canceled' } }
      : block
  ))
}

/**
 * withCancelledTurn closes what a stopped turn had streamed. A turn that said
 * nothing before it was stopped still needs a bubble saying so, which is the one
 * sentence the surface has for it.
 */
/**
 * NOTHING_SAID_YET is what a turn shows while its run has produced no text.
 * It is a placeholder, never the turn's text: a run that ends without saying
 * anything has no content, not three dots.
 */
const NOTHING_SAID_YET = '...'

/**
 * withRunError ends a turn on its failure: what streamed is closed, and the
 * error is the turn's last block — drawn in the conversation, above the worked
 * line, the way the terminal draws it. A run reports its failure once on the
 * page however many channels carry it, so the block is keyed by the run.
 */
export function withRunError(message: ChatMessage, runId: string | null | undefined, text: string, detail: ProviderErrorDetail | null, occurredAt?: unknown): ChatMessage {
  const id = String(runId ?? '').trim() ? `error:${String(runId).trim()}` : undefined
  const closed = closeStreamedBlocks(message.blocks ?? [], ['assistant', 'thinking'], occurredAt)
  if (id && closed.some((block) => block.kind === 'error' && block.id === id)) {
    return { ...message, blocks: closed }
  }
  const block: TimelineBlock = { kind: 'error', ...(id ? { id } : {}), text, ...(detail ? { detail } : {}) }
  return { ...message, blocks: [...closed, block] }
}

export function withCancelledTurn(message: ChatMessage, occurredAt?: unknown): ChatMessage {
  const blocks = cancelPendingToolBlocks(
    closeStreamedBlocks(message.blocks ?? [], ['assistant', 'thinking'], occurredAt),
  )
  const said = spokenText(blocks)
  return { ...message, blocks, content: said.trim() ? said : translate('chat.cancelled') }
}

/**
 * settleFinalAnswerBlock reconciles a turn's last words with the answer the run
 * reported.
 *
 * A run that streamed its answer already has it on the timeline, split around
 * whatever it called in between, so the reported answer adds nothing. A run that
 * streamed nothing - a provider that only returns a finished message - has no
 * block at all, and this is where its answer becomes one. The one case in
 * between is a single uninterrupted response the stream under-delivered: there
 * the reported answer is the longer, complete text and replaces what arrived.
 */
export function settleFinalAnswerBlock(blocks: TimelineBlock[], finalText: string): TimelineBlock[] {
  const text = finalText.trim()
  if (!text) return blocks
  const spoken = blocks.filter((block) => block.kind === 'assistant')
  if (spoken.length === 0) {
    return [...blocks, { kind: 'assistant', text: finalText, open: false }]
  }
  if (blocks.length !== 1 || spoken[0].kind !== 'assistant' || spoken[0].text.length >= finalText.length) {
    return blocks
  }
  return [{ ...spoken[0], text: finalText }]
}

/**
 * upsertToolBlock replaces the card for a call in place, or opens a new one.
 * A completion must not add a second card for a call already on screen.
 */
export function upsertToolBlock(blocks: TimelineBlock[], step: SubagentToolStep, eventId?: string): TimelineBlock[] {
  const index = blocks.findIndex((block) => block.kind === 'tool' && block.step.stepId === step.stepId)
  if (index >= 0) {
    return blocks.map((block, i) => (
      i === index && block.kind === 'tool'
        ? { kind: 'tool' as const, id: block.id ?? eventId, step: { ...block.step, ...step } }
        : block
    ))
  }
  return [...blocks, { kind: 'tool', id: eventId, step }]
}

/**
 * upsertApprovalBlock records a gate, or updates the one already holding that
 * action. The action id is the identity: a decision must land on the card the
 * question opened, never beside it.
 *
 * A new gate is placed directly above the call it held, which is where the
 * terminal puts it - "✔ You approved …" reads before "● shell ran". Live that
 * call is the block just opened, so above it is also the end of the timeline;
 * on a reload the whole transcript is already there and the position has to be
 * found. Both arrive at the same place, which is what makes a reloaded
 * conversation identical to the one the user watched.
 */
export function upsertApprovalBlock(
  blocks: TimelineBlock[],
  next: Extract<TimelineBlock, { kind: 'approval' }>,
): TimelineBlock[] {
  const index = blocks.findIndex((block) => block.kind === 'approval' && block.actionId === next.actionId)
  if (index >= 0) {
    return blocks.map((block, i) => (
      i === index && block.kind === 'approval' ? { ...block, ...next, id: block.id || next.id } : block
    ))
  }
  const gated = next.toolStepId
    ? blocks.findIndex((block) => block.kind === 'tool' && block.step.stepId === next.toolStepId)
    : -1
  if (gated >= 0) return [...blocks.slice(0, gated), next, ...blocks.slice(gated)]
  // A gate whose own card is withheld - the exit-plan gate holds no card -
  // still lands above the calls of the turn it belongs to, the same place the
  // terminal prints the line for it.
  const lastTool = blocks.map((block) => block.kind === 'tool').lastIndexOf(true)
  if (lastTool >= 0) return [...blocks.slice(0, lastTool), next, ...blocks.slice(lastTool)]
  return [...blocks, next]
}

/**
 * One block of a transcript, in the order it happened. One model serves the
 * conversation and every subagent, because they are the same thing: the
 * terminal retains frames in arrival order — thinking, text, and tool calls
 * interleaved exactly as they occurred — and the web shows the same record.
 * Flattening the text into one paragraph and the tools into a list beside it
 * would lose which text came before which call, which is most of what makes a
 * transcript readable.
 *
 * The conversation never produces a `prompt` block (nobody dispatched it) and a
 * subagent never produces the plan blocks the conversation renders outside this
 * timeline; every other kind is common to both.
 */
export type TimelineBlock =
  | { kind: 'prompt'; id?: string; text: string }
  /**
   * A message the user sent this subagent from its own view: the same
   * transcript position a dispatching prompt takes, drawn as the user's own
   * message rather than the agent's instruction.
   */
  | { kind: 'user'; id?: string; text: string }
  | { kind: 'assistant'; id?: string; text: string; open: boolean }
  | { kind: 'thinking'; id?: string; text: string; open: boolean; startedAt: number; durationMs?: number }
  | { kind: 'tool'; id?: string; step: SubagentToolStep }
  | { kind: 'plan'; id?: string; plan: PlanUpdateData }
  /**
   * One execution's closing line: how long it ran, when it finished, and the
   * checklist state it ended with — the same line the conversation closes its
   * own runs with, drawn by the same component.
   */
  | { kind: 'worked'; id: string; durationMs: number; finishedAt: string; plan?: WorkedPlanProgress }
  | {
      kind: 'approval'
      id: string
      actionId: string
      actionKind: string
      status: string
      /** The one line the surface printed for the decision, when it printed one. */
      confirmation?: string
      message?: string
      /** The call this gate held, when the record names one. */
      toolStepId?: string
    }
  /**
   * What ended a run in failure, as the last thing its turn says before the
   * worked line. detail carries the classified provider facts, rendered in the
   * viewer's language; text is the runtime's sentence for the rest.
   */
  | { kind: 'error'; id?: string; text: string; detail?: ProviderErrorDetail }
  /** A compaction of this history, drawn as one card from start to end. */
  | { kind: 'compaction'; id: string; compaction: ForebrainCompaction }
  /** A line of a /goal: how it opened, a continuation round, or how it ended. */
  | { kind: 'goal'; id: string; goal: ForebrainGoalLine }
  /**
   * One subagent execution dispatched with no call of its own — a plan
   * review, a runtime dispatch, a legacy ledger record. It is the same card
   * a dispatching call draws, keyed by its execution, and the id makes it
   * idempotent across the event deliveries that name it twice.
   */
  | { kind: 'subagent'; id: string; stepId: string; step: SubagentToolStep }

/**
 * A subagent's own transcript.
 *
 * A subagent talks to the agent that dispatched it, not to the user, so none of
 * this belongs in the conversation the user is having: the terminal keeps it on
 * a separate screen the user opens from the subagent's card, and this is the
 * web surface's equivalent. `task` is the dispatching agent's prompt, which is
 * the first message of that transcript; `text` is what the subagent answered.
 */
export interface SubagentTranscript {
  agentId: string
  agentType: string
  /** What its dispatch called it, e.g. "Goal check". */
  title?: string
  task: string
  status: 'running' | 'waiting_approval' | 'waiting_input' | 'done' | 'failed' | 'cancelled' | 'interrupted'
  error?: string
  /** The transcript itself, in the order the subagent produced it. */
  blocks: TimelineBlock[]
  inputTokens: number
  outputTokens: number
  /**
   * This subagent's own context budget — how much of its window is left,
   * computed on the model it runs on (plan 006). Read when its view opens and
   * kept current by the token_budget_updated events that carry its roster key.
   */
  tokenBudget?: TokenBudgetData
  startedAt?: string
  finishedAt?: string
  /**
   * The model this execution's spawn announced — the one the run actually
   * uses, as the engine resolved it once. Absent when the spawn predates
   * executions carrying models, and then the view names none rather than
   * borrowing the conversation's.
   */
  modelProvider?: string
  model?: string
  reasoningEffort?: string
  /**
   * The agent's own checklist state, the facts its working line shows and its
   * next worked line closes with. A list spans the agent's executions.
   */
  plan?: WorkedPlanProgress
  /** When each execution started, by its id — the clock a worked line counts. */
  executionStarts?: Record<string, string>
  /**
   * Bumped on every change. A view the user is not looking at uses it to show
   * that the subagent has produced something since they last opened it, the
   * way the terminal counts unseen messages on a background screen.
   */
  updatedSeq: number
}

function emptySubagentTranscript(agentId: string): SubagentTranscript {
  return {
    agentId,
    agentType: '',
    task: '',
    status: 'running',
    blocks: [],
    inputTokens: 0,
    outputTokens: 0,
    updatedSeq: 0,
  }
}

/**
 * appendStreamedText grows the open block of this kind, or opens a new one.
 *
 * The terminal buffers streamed text and flushes it as one block when something
 * else interrupts — a tool call, or the end of the run. Closing the block on
 * those same boundaries is what reproduces its transcript: text, then the call
 * it led to, then the next text, rather than one paragraph with the calls
 * hanging off the side.
 */
function eventTimestamp(raw: unknown): number {
  const parsed = Date.parse(String(raw ?? ''))
  return Number.isFinite(parsed) ? parsed : Date.now()
}

function responseStatus(error: unknown): number | undefined {
  const status = Number((error as { response?: { status?: unknown } } | null)?.response?.status)
  return Number.isFinite(status) && status > 0 ? status : undefined
}

function appendStreamedText(
  blocks: TimelineBlock[],
  kind: 'assistant' | 'thinking',
  text: string,
  eventId?: string,
  occurredAt?: unknown,
): TimelineBlock[] {
  const last = blocks[blocks.length - 1]
  if (last && last.kind === kind && last.open) {
    const grown = { ...last, text: last.text + text }
    return [...blocks.slice(0, -1), grown]
  }
  if (kind === 'thinking') {
    return [...blocks, { kind, id: eventId, text, open: true, startedAt: eventTimestamp(occurredAt) }]
  }
  return [...blocks, { kind, id: eventId, text, open: true }]
}

function closeStreamedBlocks(
  blocks: TimelineBlock[],
  kinds: ('assistant' | 'thinking')[],
  occurredAt?: unknown,
): TimelineBlock[] {
  const closedAt = eventTimestamp(occurredAt)
  return blocks.map((block) => {
    if (block.kind === 'assistant' && block.open && kinds.includes('assistant')) {
      return { ...block, open: false }
    }
    if (block.kind === 'thinking' && block.open && kinds.includes('thinking')) {
      return { ...block, open: false, durationMs: Math.max(0, closedAt - block.startedAt) }
    }
    return block
  })
}

/**
 * subagentIdFromPayload reports which agent an event belongs to, empty for the
 * primary agent. Text-shaped events carry the roster key at the top level;
 * tool steps carry it on the meta, which is where the runtime tags every step a
 * subagent runs.
 */
export function subagentIdFromPayload(payload: Record<string, unknown>): string {
  const direct = String(payload.agentId ?? '').trim()
  if (direct) return direct
  const meta = payload.toolMeta
  if (meta && typeof meta === 'object') {
    return String((meta as Record<string, unknown>).agentId ?? '').trim()
  }
  return ''
}

function subagentStepMeta(payload: Record<string, unknown>): Record<string, unknown> {
  return (payload.toolMeta && typeof payload.toolMeta === 'object')
    ? payload.toolMeta as Record<string, unknown>
    : {}
}

function subagentStepStatus(payload: Record<string, unknown>, completed: boolean): string {
  const status = String(subagentStepMeta(payload).status ?? '').trim()
  if (status) return status
  return completed ? 'completed' : 'running'
}

/**
 * normalizeApprovalDecision is the one spelling both surfaces store for an
 * approval's outcome. The event carries the action's own status, which is already
 * the lowercase form the terminal shows; this keeps a payload that spells it
 * differently from rendering as a third, unknown state.
 */
export function normalizeApprovalDecision(raw: string): string {
  return String(raw ?? '').trim().toLowerCase() || 'resolved'
}

/**
 * subagentStepSummary is the one line the terminal puts on a tool card
 * ("running git diff", "ran read_file · 42 bytes"). The runtime computes it and
 * sends it with the step, so both surfaces label the same call the same way;
 * the invocation and the tool name stand in when an older payload lacks it.
 */
function subagentStepSummary(payload: Record<string, unknown>): string {
  const meta = subagentStepMeta(payload)
  const summary = String(payload.summary ?? '').trim()
  if (summary) return summary
  const invocation = String(meta.invocation ?? '').trim()
  if (invocation) return invocation
  const description = String(payload.description ?? '').trim()
  if (description) return description
  return String(payload.toolName ?? '').trim()
}

export type SessionMode = 'agent' | 'plan'
export type RuntimeStatusKind = 'idle' | 'working' | 'reconnecting'

export interface RuntimeStatus {
  kind: RuntimeStatusKind
  startedAt?: number
  elapsedMs: number
  attempt?: number
  maxAttempts?: number
  message?: string
  toolName?: string
  planCompleted?: number
  planTotal?: number
  planActive?: string
  /** The round a running /goal is in. */
  goalRound?: number
  /** Set while the goal's check reads the workspace. */
  goalChecking?: boolean
}

export interface ContextRuntimeSignals {
  compactVersion: number
  budgetVersion: number
  activeRunId?: string
}

type SessionResetOptions = {
  sid: string | null
  loadMessages?: boolean
  loadMode?: boolean
}

export function emptyPendingInputPreview(): PendingInputPreview {
  return { pendingSteers: [], rejectedSteers: [], queuedMessages: [] }
}

export function emptyContextRuntimeSignals(): ContextRuntimeSignals {
  return { compactVersion: 0, budgetVersion: 0, activeRunId: undefined }
}

import { getErrorMessage, forebrainApi, type ChatAttachmentRecord, type ChatMessageRecord, type ModelCatalogListing, type RunInputResponse, type SessionContextDebug } from '@/lib/api'
import { persistLastSessionId } from '@/composables/useLastSession'
import { mergeSubmissions, type ComposerSubmission, type SubmittedAttachment } from '@/lib/composerSubmission'
import { t as translate, type I18nKey } from '@/locales'
import { AUTO_CONTINUE_EVENT_TYPES, parseAutoContinue, type AutoContinueEntry, type AutoContinueState } from '@/lib/autoContinue'
import { LSP_RECOMMENDATION_EVENT_TYPE, parseLspRecommendation, type LspRecommendation } from '@/lib/lspRecommendation'
import {
  buildBrowserForebrainGatewayChatWsUrl,
  isSupportedForebrainRunEventSchema,
  isTerminalRunEventType,
  parseForebrainGatewayServerHello,
  parseForebrainMcpStatusMessage,
  parseForebrainRunEventMessage,
  parseForebrainSessionBoundMessage,
  compactionFromHistoryRow,
  goalFromHistoryRow,
  parseCompactionEvent,
  parseGoalEvent,
  FOREBRAIN_GOAL_CHECK_AGENT_TYPE,
  FOREBRAIN_GOAL_EVENT_TYPES,
  parseTokenBudgetPayload,
  FOREBRAIN_COMPACTION_EVENT_TYPES,
  parseTurnDiffPayload,
  FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
  FOREBRAIN_RUN_EVENT_SCHEMA_VERSION,
  type ForebrainCompaction,
  type ForebrainCompactionUpdate,
  type ForebrainGoalLine,
  type ForebrainRunEvent,
  type ForebrainMcpStatus,
} from '@/lib/forebrainGatewayRuntime'

type TranslateFn = (key: I18nKey, params?: Record<string, string | number>) => string

export interface SendOptions {
  sessionId?: string
  modelId?: string
  createBy?: string
  /**
   * What the message attached: uploaded files, and the workspace images the
   * composer's @ picker resolved. The images are sent alongside the text
   * rather than named in it, so the server never has to parse mentions back
   * out of the prompt.
   */
  attached?: Pick<ComposerSubmission, 'attachments' | 'mentionImages'>
  activeInputDisposition?: 'steer' | 'queue'
  /**
   * A pick made in the picker a slash command offered; the message is then
   * that command, and no user bubble is drawn for it — the picked card says
   * what was chosen.
   */
  choice?: SlashChoice
  /**
   * The skill this turn activates — the workshop's equivalent of the
   * terminal's slash handoff. Sent on the first message of a task and
   * validated by the server against the live skill set.
   */
  skillName?: string
  skillPath?: string
}

/** A message a run handed back as it ended, as the gateway reports it. */
interface ReleasedInput {
  text: string
  attachments: string[]
  mentionImages: string[]
}

/** What the conversation knows about one run while projecting its events. */
interface RunProjectionState {
  fullAnswer: string
  runStartedAt?: string
  completedNormally?: boolean
  completedRunId?: string | null
  /** Set for a run this page started: its hand-back is this page's to act on. */
  sentHere?: boolean
  /** The send that started the run has yet to act on what its run hands back. */
  awaitingRelease?: boolean
  released?: ReleasedInput[]
  /**
   * The answer the run's output is drawn into once a steer has been handed to
   * its model: each delivered steer closes the answer so far and opens a new
   * one after it. Unset until the first, when the output goes to the answer
   * the run started with.
   */
  answerMessageId?: string
}

/** The message a run's output is drawn into now (see RunProjectionState.answerMessageId). */
function answerTarget(state: RunProjectionState, startedWith: string): string {
  return state.answerMessageId || startedWith
}

/**
 * A message waiting on the send in flight: sent before that send's run
 * existed — while the conversation was compacted for it, or a command ran —
 * or handed back by its run as it ended. It follows that send: into its run's
 * queue once the run starts, on as the next send when it ends normally, or
 * back to the composer with it when it is withdrawn or fails. settle tells
 * whoever sent it that it has been dealt with.
 */
interface HeldSend {
  text: string
  options: SendOptions
  settle: () => void
}

/** What a sent message attached, the way its history row names it. */
function messageAttachments(attached?: SendOptions['attached']): ChatAttachmentRecord[] {
  return [
    ...(attached?.attachments ?? []).map((attachment) => ({ fileId: attachment.fileId, name: attachment.filename, mediaType: attachment.mediaType })),
    ...(attached?.mentionImages ?? []).map((path) => ({ path, name: path.split('/').filter(Boolean).pop() ?? path })),
  ]
}

/**
 * What a message that says nothing but what it attached shows, as the
 * gateway stores it: a marker for each upload, then one for each image.
 */
function attachedOnlyText(attachments: ChatAttachmentRecord[]): string {
  let images = 0
  return attachments
    .map((attachment) => (attachment.fileId ? `[Attachment ${attachment.name}]` : `[Image #${++images}]`))
    .join('\n')
}

function submissionOf(text: string, options?: SendOptions): ComposerSubmission {
  return {
    text,
    attachments: options?.attached?.attachments ?? [],
    mentionImages: options?.attached?.mentionImages ?? [],
  }
}

/** How a held message reads in the queue, as the gateway previews queued input. */
function heldPreviewText(held: HeldSend): string {
  const text = held.text.trim()
  if (text) return text
  const attached = (held.options.attached?.attachments.length ?? 0) + (held.options.attached?.mentionImages.length ?? 0)
  return attached === 1 ? '1 attachment' : `${attached} attachments`
}

export function applyAssistantRunEventMetadata(
  message: ChatMessage,
  evt: Pick<ForebrainRunEvent, 'type' | 'payload'>,
): ChatMessage {
  const payload = normalizeRunEventPayload(evt.payload)
  switch (evt.type) {
    case 'token_budget_updated': {
      const tokenBudget = parseTokenBudgetPayload(payload)
      return tokenBudget ? { ...message, tokenBudget } : message
    }
    case 'plan_updated': {
      const planUpdate = parsePlanUpdatePayload(payload)
      return planUpdate ? applyPlanUpdateToMessage(message, planUpdate) : message
    }
    default:
      return message
  }
}

function normalizeRunEventPayload(raw: unknown): Record<string, unknown> | undefined {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return undefined
  return toCamelCase(raw as Record<string, unknown>) as Record<string, unknown>
}

function stringList(v: unknown): string[] {
  if (!Array.isArray(v)) return []
  return v.map((item) => String(item ?? '').trim()).filter(Boolean)
}

function parsePendingInputPreview(raw: unknown): PendingInputPreview {
  const payload = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw as Record<string, unknown> : {}
  const data = payload.data && typeof payload.data === 'object' && !Array.isArray(payload.data)
    ? payload.data as Record<string, unknown>
    : payload
  return {
    pendingSteers: stringList(data.pendingSteers ?? data.pending_steers),
    rejectedSteers: stringList(data.rejectedSteers ?? data.rejected_steers),
    queuedMessages: stringList(data.queuedMessages ?? data.queued_messages),
  }
}

/** The messages a run's end sends as the next turn, as the gateway reports them. */
function releasedNextInputs(payload: Record<string, unknown>): ReleasedInput[] {
  const inputs = Array.isArray(payload.next) ? payload.next : []
  return inputs.flatMap((raw) => {
    if (!raw || typeof raw !== 'object') return []
    const input = raw as Record<string, unknown>
    return [{
      text: String(input.text ?? ''),
      attachments: stringList(input.attachments),
      mentionImages: stringList(input.mentionImages),
    }]
  })
}

function releasedInputs(payload: Record<string, unknown>): ReleasedInput[] {
  const inputs = Array.isArray(payload.inputs) ? payload.inputs : []
  return inputs.flatMap((raw) => {
    if (!raw || typeof raw !== 'object') return []
    const input = raw as Record<string, unknown>
    return [{
      text: String(input.text ?? ''),
      attachments: stringList(input.attachments),
      mentionImages: stringList(input.mentionImages),
    }]
  })
}

function parsePlanUpdatePayload(raw: unknown): PlanUpdateData | null {
  const payload = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw as Record<string, unknown> : null
  if (!payload) return null
  const data = payload.data && typeof payload.data === 'object' && !Array.isArray(payload.data)
    ? payload.data as Record<string, unknown>
    : payload
  const rawItems = Array.isArray(data.items) ? data.items : []
  const items = rawItems
    .map((item) => item && typeof item === 'object' && !Array.isArray(item) ? item as Record<string, unknown> : null)
    .filter((item): item is Record<string, unknown> => Boolean(item))
    .map((item) => ({
      id: typeof item.id === 'string' ? item.id : undefined,
      content: String(item.content ?? '').trim(),
      status: String(item.status ?? 'pending').trim() || 'pending',
      active: typeof item.active === 'string' ? item.active.trim() || undefined : undefined,
    }))
    .filter((item) => item.content)
  const completed = typeof data.completed === 'number'
    ? data.completed
    : typeof data.completed === 'string'
      ? Number.parseInt(data.completed, 10)
      : undefined
  const total = typeof data.total === 'number'
    ? data.total
    : typeof data.total === 'string'
      ? Number.parseInt(data.total, 10)
      : undefined
  const explanation = String(data.explanation ?? '').trim()
  return {
    title: String(data.title ?? translate('chat.updatedPlan')).trim() || translate('chat.updatedPlan'),
    explanation: explanation || undefined,
    completed: Number.isFinite(completed) ? completed : undefined,
    total: Number.isFinite(total) ? total : undefined,
    items,
    active: String(data.active ?? '').trim() || undefined,
    agentId: String(data.agentId ?? '').trim() || undefined,
  }
}

function compactionBlockId(compactionId: string): string {
  return `compaction:${compactionId}`
}

function goalBlockId(goal: ForebrainGoalLine, position: number): string {
  return goal.phase === 'round' ? `goal:round:${goal.round ?? position}` : `goal:${goal.phase}:${position}`
}

/**
 * appendGoalLine adds one of a goal's lines where the timeline stands. What
 * was streamed before it is closed first, so a round's answer never runs into
 * the next one.
 */
export function appendGoalLine(blocks: TimelineBlock[], goal: ForebrainGoalLine, occurredAt?: unknown): TimelineBlock[] {
  return [
    ...closeStreamedBlocks(blocks, ['assistant', 'thinking'], occurredAt),
    { kind: 'goal', id: goalBlockId(goal, blocks.length), goal },
  ]
}

/**
 * applyCompactionUpdate moves a compaction's card: the first event of a
 * compaction adds it where the timeline stands, and every later one updates it
 * in place. Its percentage only rises, and a finished compaction reads 100.
 * Text streamed before the card is closed first, as it is before a tool call.
 */
export function applyCompactionUpdate(blocks: TimelineBlock[], update: ForebrainCompactionUpdate, occurredAt?: unknown): TimelineBlock[] {
  const index = blocks.findIndex((block) => block.kind === 'compaction' && block.compaction.compactionId === update.compactionId)
  const defined = Object.fromEntries(Object.entries(update.patch).filter(([, value]) => value !== undefined)) as Partial<ForebrainCompaction>
  if (index < 0) {
    const compaction: ForebrainCompaction = { compactionId: update.compactionId, status: 'running', percent: 0, ...defined }
    return [
      ...closeStreamedBlocks(blocks, ['assistant', 'thinking'], occurredAt),
      { kind: 'compaction', id: compactionBlockId(update.compactionId), compaction },
    ]
  }
  return blocks.map((block, i) => {
    if (i !== index || block.kind !== 'compaction') return block
    const next: ForebrainCompaction = { ...block.compaction, ...defined }
    next.percent = next.status === 'done' ? 100 : Math.max(block.compaction.percent, defined.percent ?? 0)
    return { ...block, compaction: next }
  })
}

export function applyPlanUpdateToMessage(message: ChatMessage, planUpdate: PlanUpdateData): ChatMessage {
  return {
    ...message,
    planUpdates: [...(message.planUpdates ?? []), planUpdate],
  }
}

export function computeWorkedDurationMs(startedAt?: string, finishedAt?: string): number | undefined {
  if (!startedAt || !finishedAt) return undefined
  const start = Date.parse(startedAt)
  const end = Date.parse(finishedAt)
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return undefined
  return end - start
}

/** The checklist facts of a run's worked line, as the engine reported them. */
export interface WorkedPlanProgress {
  done: number
  total: number
  active?: string
}

/**
 * The line that closes every run, however long and however it ended — the
 * terminal's "Worked for" line, as segments the template joins with " · ":
 * the duration (a sub-second run counts as 1s), the checklist progress when
 * the turn had one, then the minute it finished when that is known.
 */
export interface WorkedLine {
  label: string
  plan?: WorkedPlanProgress
  time?: string
}

export function formatWorkedDurationLabel(durationMs?: number, tr: TranslateFn = translate, finishedAt?: string, plan?: WorkedPlanProgress): WorkedLine {
  if (durationMs == null || !Number.isFinite(durationMs) || durationMs < 0) return { label: '' }
  let totalSeconds = Math.floor(durationMs / 1000)
  if (totalSeconds === 0 && durationMs > 0) totalSeconds = 1
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  const duration = hours > 0
    ? `${hours}h ${String(minutes).padStart(2, '0')}m ${String(seconds).padStart(2, '0')}s`
    : minutes > 0
      ? `${minutes}m ${String(seconds).padStart(2, '0')}s`
      : `${seconds}s`
  const finished = finishedAt ? new Date(finishedAt) : null
  const time = finished && !Number.isNaN(finished.getTime())
    ? `${String(finished.getHours()).padStart(2, '0')}:${String(finished.getMinutes()).padStart(2, '0')}`
    : undefined
  return {
    label: tr('chat.workedFor', { duration }),
    plan: plan && plan.total > 0 ? plan : undefined,
    time,
  }
}

/**
 * The worked line that closes an assistant turn: duration, checklist progress
 * when the run had one, then the finish time. Every surface that shows a
 * conversation draws it from this, so a run reads the same wherever it ran.
 */
export function workedLineOf(
  msg: Pick<ChatMessage, 'role' | 'workedDurationMs' | 'runFinishedAt' | 'workedPlanDone' | 'workedPlanTotal' | 'workedPlanActive'>,
  tr: TranslateFn = translate,
): WorkedLine {
  if (msg.role !== 'assistant') return { label: '' }
  const plan = msg.workedPlanTotal && msg.workedPlanTotal > 0
    ? { done: msg.workedPlanDone ?? 0, total: msg.workedPlanTotal, active: msg.workedPlanActive }
    : undefined
  return formatWorkedDurationLabel(msg.workedDurationMs, tr, msg.runFinishedAt, plan)
}

/** Join a WorkedLine's segments with the worked line's separator. */
export function workedLineText(line: WorkedLine): string {
  return [
    line.label,
    line.plan ? `${line.plan.done}/${line.plan.total}` : '',
    line.plan?.active ?? '',
    line.time ?? '',
  ]
    .filter(Boolean)
    .join(' · ')
}

/**
 * What a run's end records on its answer, whatever ended it — finished, failed
 * or stopped: when it finished and how long it worked.
 */
function runEndPatch(finishedAt: string | undefined, elapsedMs: unknown, runStartedAt: string | undefined, plan?: WorkedPlanProgress): Partial<ChatMessage> {
  const elapsed = Number(elapsedMs)
  const workedDurationMs = elapsedMs != null && Number.isFinite(elapsed) && elapsed >= 0
    ? elapsed
    : computeWorkedDurationMs(runStartedAt, finishedAt)
  return {
    ...(finishedAt ? { runFinishedAt: finishedAt } : {}),
    ...(workedDurationMs != null ? { workedDurationMs } : {}),
    ...(plan && plan.total > 0 ? { workedPlanDone: plan.done, workedPlanTotal: plan.total, workedPlanActive: plan.active } : {}),
  }
}

/** The engine's checklist facts off a run-end payload, unverified shapes dropped. */
function planProgressOfPayload(payload: Record<string, unknown>): WorkedPlanProgress | undefined {
  const done = Number(payload.planDone)
  const total = Number(payload.planTotal)
  if (!Number.isFinite(total) || total <= 0) return undefined
  const active = typeof payload.planActive === 'string' && payload.planActive.trim() ? payload.planActive : undefined
  return { done: Number.isFinite(done) ? done : 0, total, active }
}

export function formatRuntimeDuration(durationMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(durationMs / 1000))
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours}h ${String(minutes).padStart(2, '0')}m ${String(seconds).padStart(2, '0')}s`
  if (minutes > 0) return `${minutes}m ${String(seconds).padStart(2, '0')}s`
  return `${seconds}s`
}

/**
 * The final elapsed of an ended task — the terminal's formatDuration
 * (pkg/tui/render.go), spelled the same so both surfaces say "4.2s", "14m3s"
 * and "<0.1s" about the same work. Zero renders nothing: a task that never
 * ran has no elapsed time to claim.
 */
export function formatToolDuration(durationMs: number): string {
  if (!(durationMs > 0)) return ''
  if (durationMs < 100) return '<0.1s'
  const seconds = durationMs / 1000
  if (seconds >= 60) return `${Math.floor(seconds / 60)}m${Math.floor(seconds) % 60}s`
  if (seconds >= 10) return `${Math.floor(seconds)}s`
  return `${seconds.toFixed(1)}s`
}

/** One task of a subagent_* call's facts, mirroring the engine's event.SubagentCallTask. */
export interface SubagentCallTaskFacts {
  index: number
  /** The roster key of the agent the task runs as, when known. */
  key?: string
  title?: string
  agentType?: string
  /** waiting | running | done | failed | cancelled | skipped, or '' while unsaid. */
  status?: string
  error?: string
  timedOut?: boolean
  stopRequested?: boolean
  executionId?: string
  /** The execution's clock as the result reported it, unix seconds. */
  startedAt?: number
  finishedAt?: number
}

/** What one subagent_* tool call is about — the card facts the engine derived once. */
export interface SubagentCall {
  /** run | send | continue | status | wait | close | list | review */
  verb: string
  tasks: SubagentCallTaskFacts[]
}

/**
 * The lifecycle state of one card task as the live event stream bound it: a
 * dispatched execution's own clock and tool progress, settled by its end.
 * What the stream never bound comes from the step's facts instead.
 */
export interface SubagentCallTaskLive {
  agentId: string
  executionId?: string
  title?: string
  agentType?: string
  status?: string
  error?: string
  /** The bound execution's spawn, epoch ms — the row clock starts here. */
  startedAtMs?: number
  /** The bound execution's stop, epoch ms — from the end's finishedAtMs. */
  finishedAtMs?: number
  toolCount?: number
  latestTool?: string
  /** Step ids already counted, so a completion never counts twice. */
  toolStepIds?: string[]
}

/** One row of a subagent card, ready for the locale and the icons. */
export interface SubagentCallRowView {
  status: 'waiting' | 'running' | 'done' | 'failed' | 'cancelled' | 'skipped'
  title: string
  agentId?: string
  clock?: { kind: 'running'; startedAtMs: number; label: string } | { kind: 'final'; label: string }
  latestTool?: string
  moreToolUses?: number
  listType?: string
  timedOut?: boolean
  stopRequested?: boolean
  error?: string
}

/** One subagent_* card's view model: header, rows, and the clock it still owes. */
export interface SubagentCallView {
  /** The locale key the header sentence comes from, e.g. subagentCard.send.done. */
  headerKey: string
  /** The task count the header names; null where the phrase takes none. */
  headerCount: { count: number; type: string; one: boolean } | null
  /** The call's own elapsed, when the header is the place that carries it. */
  headerDuration?: string
  /** The label a canceled call says it was doing; set when headerKey is …canceled. */
  canceledLabel?: string
  /** The call's own failure, shown once under the header. */
  callError?: string
  breakdown?: { done: number; failed: number; cancelled: number; skipped: number }
  rows: SubagentCallRowView[]
  /** True when the card has no rows at all — the shared empty line. */
  empty: boolean
  hasLiveClock: boolean
}

const SUBAGENT_QUERY_VERBS = new Set(['status', 'wait', 'close', 'list'])

function subagentTaskTerminal(status: string): boolean {
  return status === 'done' || status === 'failed' || status === 'cancelled' || status === 'skipped'
}

/** The status a subagent's end settles its task with, mirroring the terminal's reducer. */
function subagentEndStatus(status: string, error: string): 'done' | 'failed' | 'cancelled' {
  if (status === 'cancelled') return 'cancelled'
  if (status === 'failed' || error) return 'failed'
  return 'done'
}

/**
 * subagentCallView is the one derivation of a subagent card's shape: which
 * header sentence, which rows, and each row's clock. It is pure — the live
 * bindings and `now` come in as arguments — so every surface of the web draws
 * the same card from the same facts, the way the terminal's reducer does for
 * its own card. The semantics are the terminal's (plan 013): a bound task's
 * lifecycle outranks the call's facts, a query card is the snapshot its call
 * took, and a header carries a duration only where its rows do not.
 */
export function subagentCallView(
  step: SubagentToolStep,
  live?: ReadonlyMap<number, SubagentCallTaskLive>,
  now: number = Date.now(),
): SubagentCallView {
  const call = step.subagentCall ?? { verb: 'run', tasks: [] }
  const verb = String(call.verb || 'run')
  const query = SUBAGENT_QUERY_VERBS.has(verb)
  const rows: SubagentCallRowView[] = []
  for (const facts of call.tasks ?? []) {
    const bound = live?.get(Number(facts.index))
    const status = String(bound?.status ?? facts.status ?? '').trim() || 'waiting'
    const title = String(bound?.title ?? facts.title ?? '').trim()
    if (query && !title) {
      // A query row the call could not name yet says nothing; the result
      // will name it.
      continue
    }
    const startedAtMs = bound?.startedAtMs ?? (Number(facts.startedAt) > 0 ? Number(facts.startedAt) * 1000 : undefined)
    const finishedAtMs = bound?.finishedAtMs ?? (Number(facts.finishedAt) > 0 ? Number(facts.finishedAt) * 1000 : undefined)
    let clock: SubagentCallRowView['clock']
    if (startedAtMs != null && finishedAtMs == null && status === 'running') {
      // The walking clock every surface reads at `now`, in the terminal's
      // working-elapsed form.
      clock = { kind: 'running', startedAtMs, label: formatRuntimeDuration(Math.max(0, now - startedAtMs)) }
    } else if (startedAtMs != null && finishedAtMs != null && finishedAtMs > startedAtMs && subagentTaskTerminal(status)) {
      clock = { kind: 'final', label: formatToolDuration(finishedAtMs - startedAtMs) }
    }
    rows.push({
      status: status as SubagentCallRowView['status'],
      title,
      agentId: String(bound?.agentId ?? facts.key ?? '').trim() || undefined,
      clock,
      ...(query
        ? {
          listType: verb === 'list' ? String(bound?.agentType ?? facts.agentType ?? '').trim() || undefined : undefined,
          timedOut: Boolean(facts.timedOut),
          stopRequested: Boolean(facts.stopRequested),
        }
        : {
          latestTool: String(bound?.latestTool ?? '').trim() || undefined,
          moreToolUses: (bound?.toolCount ?? 0) > 1 ? (bound?.toolCount ?? 0) - 1 : undefined,
        }),
      ...(status !== 'waiting' && subagentTaskTerminal(status) && String(bound?.error ?? facts.error ?? '').trim()
        ? { error: String(bound?.error ?? facts.error ?? '').trim() }
        : {}),
    })
  }
  const status = String(step.status ?? '').trim().toLowerCase()
  const canceled = status.startsWith('cancel')
  const callError = !canceled && (status === 'failed' || status === 'error') && String(step.error ?? '').trim()
    ? String(step.error).trim()
    : undefined
  const running = status === '' || status === 'running' || status === 'pending' || status === 'awaiting approval'
  const anyTaskLive = rows.some((row) => row.status === 'running' || row.status === 'waiting')
  const anyTaskRunning = rows.some((row) => row.status === 'running')
  const anyTaskTerminal = rows.some((row) => subagentTaskTerminal(row.status))
  let phase: 'starting' | 'running' | 'done' | 'failed' | 'canceled'
  if (canceled) phase = 'canceled'
  else if (callError) phase = 'failed'
  else if (query) phase = running ? 'running' : 'done'
  else if (running || anyTaskLive) {
    phase = (verb === 'send' || verb === 'review') && !anyTaskRunning && !anyTaskTerminal ? 'starting' : 'running'
  } else phase = 'done'

  let headerCount: SubagentCallView['headerCount'] = null
  if (verb !== 'list' || phase === 'done') {
    const count = call.tasks?.length ?? 0
    let type = ''
    for (const facts of call.tasks ?? []) {
      const each = String(live?.get(Number(facts.index))?.agentType ?? facts.agentType ?? '').trim()
      if (!each) {
        type = ''
        break
      }
      if (!type) type = each
      else if (type !== each) {
        type = ''
        break
      }
    }
    // The terminal's count label rule: the query phrases that never name a
    // type say only the count.
    const withType = verb === 'run' || verb === 'send' || verb === 'continue' || verb === 'review' ||
      ((verb === 'status' || verb === 'wait' || verb === 'close') && phase === 'done')
    headerCount = { count, type: withType ? type : '', one: count === 1 }
  }

  let headerDuration: string | undefined
  if (phase !== 'running' && phase !== 'starting') {
    const durationMs = Number(step.durationSeconds) * 1000
    // The header names a duration only where its rows do not already: a
    // query's own call, and a multi-task batch's wall clock.
    if (Number.isFinite(durationMs) && durationMs > 0 && (query || (call.tasks?.length ?? 0) > 1)) {
      headerDuration = formatToolDuration(durationMs)
    }
  }

  let breakdown: SubagentCallView['breakdown']
  if (phase === 'done' && !query) {
    const tally = { done: 0, failed: 0, cancelled: 0, skipped: 0 }
    for (const row of rows) {
      if (row.status === 'done' || row.status === 'failed' || row.status === 'cancelled' || row.status === 'skipped') {
        tally[row.status] += 1
      }
    }
    if (tally.failed + tally.cancelled + tally.skipped > 0) breakdown = tally
  }

  return {
    headerKey: phase === 'canceled' ? 'subagentCard.canceled' : `subagentCard.${verb}.${phase}`,
    headerCount,
    headerDuration,
    ...(phase === 'canceled' ? { canceledLabel: String(step.summary ?? '').trim() || step.toolName } : {}),
    ...(callError ? { callError } : {}),
    ...(breakdown ? { breakdown } : {}),
    rows,
    empty: rows.length === 0 && (call.tasks?.length ?? 0) === 0,
    hasLiveClock: rows.some((row) => row.clock?.kind === 'running'),
  }
}

/**
 * The label of a run's live status line, plus the checklist facts that belong
 * beside it as segments (checkbox, done/total, the task in flight). The label
 * says how long and what kind of run; the plan is structure the caller draws,
 * never English baked into the sentence.
 */
export interface RuntimeStatusLabel {
  label: string
  plan?: WorkedPlanProgress
}

export function formatRuntimeStatusLabel(status: RuntimeStatus, tr: TranslateFn = translate): RuntimeStatusLabel {
  const duration = formatRuntimeDuration(status.elapsedMs)
  const plan = (status.planTotal ?? 0) > 0
    ? { done: status.planCompleted ?? 0, total: status.planTotal ?? 0, active: status.planActive || undefined }
    : undefined
  if (status.kind === 'working' && status.goalChecking) {
    return { label: tr('chat.goal.runtimeChecking', { round: status.goalRound ?? 1, duration }), plan }
  }
  if (status.kind === 'working' && status.toolName === 'shell') {
    return { label: tr('chat.runtimeShell', { duration }), plan }
  }
  if (status.kind === 'working' && (status.goalRound ?? 0) > 0) {
    return { label: tr('chat.goal.runtimeWorking', { round: status.goalRound ?? 1, duration }), plan }
  }
  if (status.kind === 'working') return { label: tr('chat.runtimeWorking', { duration }), plan }
  if (status.kind === 'reconnecting') {
    const attempt = status.attempt ?? 1
    const max = status.maxAttempts ?? 5
    return { label: tr('chat.runtimeReconnecting', { attempt, max, duration }), plan }
  }
  return { label: '', plan }
}

export function useChatStream() {
  const sessionId = ref<string | null>(null)
  const mode = ref<SessionMode>('agent')
  const modePhase = ref('')
  const plan = ref<ExecutionPlan | null>(null)
  const answer = ref('')
  const isStreaming = ref(false)
  const errorText = ref<string | null>(null)
  // The MCP startup state the gateway pushed for this socket's bound session.
  // It is live state, not conversation: it arrives on its own outbound op and
  // never enters the event log, so a reconnect re-asks for it rather than
  // replaying it.
  const mcpStatus = ref<ForebrainMcpStatus | null>(null)
  // The continuation the runtime is waiting to run once a usage limit resets.
  // Live state like mcpStatus: the observer's binding says what is pending
  // now, and only events after that binding change it. subagentAutoContinue
  // holds each subagent's own, keyed by its roster key, so a continuation
  // shows in the subagent's view rather than the conversation's.
  const autoContinue = ref<AutoContinueState | null>(null)
  const subagentAutoContinue = ref<Record<string, AutoContinueState>>({})
  // The language-server recommendation waiting for the user's answer. Like
  // autoContinue it belongs to the conversation, not to any run's timeline,
  // and only a live event sets it: history replays the answer's transcript
  // line, never a second offer.
  const lspRecommendation = ref<LspRecommendation | null>(null)
  const errorDetail = ref<ProviderErrorDetail | null>(null)
  // A failed turn arrives as both a rendered English sentence and the
  // classified facts behind it. Rendering from the facts here is what makes the
  // message follow the viewer's language -- including a switch made while the
  // error is already on screen -- and the sentence is the fallback for failures
  // the runtime could not classify.
  const error = computed<string | null>({
    get() {
      const detail = errorDetail.value
      if (detail) {
        const localized = formatProviderError(detail)
        if (localized) return localized
      }
      return errorText.value
    },
    // Writable so the composable still exposes a plain settable message: a
    // caller assigning literal text is replacing the failure, not annotating
    // the classified one, so the detail goes with it.
    set(value) {
      setError(value)
    },
  })
  function setError(text: string | null, detail: ProviderErrorDetail | null = null) {
    errorText.value = text
    errorDetail.value = detail
  }
  // The conversation and the subagents' views are replaced whole on every
  // change and never mutated in place, so they are held shallow: a deep ref
  // would wrap every message, block and card in a proxy, and a reload that
  // folds hundreds of subagent events into a long conversation would pay for
  // that proxy on each of them.
  const messages = shallowRef<ChatMessage[]>([])
	const historyLoading = ref(false)
	const historyError = ref<string | null>(null)
  const pendingActionsVersion = ref(0)
  // What the gateway reports queued for the active run, and what this client
  // holds for a run that has not started yet; the queue shows both.
  const serverPendingInput = ref<PendingInputPreview>(emptyPendingInputPreview())
  // A subagent's own queued input, keyed by its roster id: what its view shows
  // where the conversation's queue preview sits (plan 007's per-agent queue).
  const subagentPendingInput = ref<Record<string, PendingInputPreview>>({})
  // Messages the boundary decided go back to one subagent's composer, keyed by
  // its roster id, until that view takes them.
  const subagentReturnedDraft = ref<Record<string, ComposerSubmission>>({})
  const heldBeforeRun = ref<HeldSend[]>([])
  // Messages on their way back to the composer, which takes them as one draft.
  const returnedDraft = ref<ComposerSubmission | null>(null)
  const returnedNotice = ref<string | null>(null)
  const returnedNoticeCode = ref<string | null>(null)
  const pendingInputPreview = computed<PendingInputPreview>(() => (heldBeforeRun.value.length
    ? { ...serverPendingInput.value, queuedMessages: [...serverPendingInput.value.queuedMessages, ...heldBeforeRun.value.map(heldPreviewText)] }
    : serverPendingInput.value))
  const runtimeStatus = ref<RuntimeStatus>({ kind: 'idle', elapsedMs: 0 })
  const contextSignals = ref<ContextRuntimeSignals>({ compactVersion: 0, budgetVersion: 0 })
  // Kept for the whole session, not per turn: a finished subagent stays
  // readable after the turn that dispatched it ends, which is the only way to
  // go back and read what it concluded.
  const subagents = shallowRef<SubagentTranscript[]>([])
  let streamAbortController: AbortController | null = null
  let activeSocket: WebSocket | null = null
  let activeRunId: string | null = null
  let sessionObserver: WebSocket | null = null
  let observerSessionId = ''
  // The last sequence the observer's binding replayed: an auto-continue event
  // at or below it is history the binding's own snapshot already accounts for.
  let observerHighWater = 0
  // The sequence of the auto-continue event autoContinue was last set from. A
  // send's own socket can deliver an event before the observer's binding
  // arrives; a snapshot older than that event must not undo it.
  let autoContinueSequence = 0
  let historyGeneration = 0
  let sessionEventCursor = 0
  let historyLoadingSession = ''
  const bufferedSessionEvents: ForebrainRunEvent[] = []
  const appliedEventIDs = new Set<string>()
  const legacyMirroredRunIDs = new Set<string>()
  const eventProjectionByRun = new Map<string, {
    assistantMessageId: string
    planBlocks: PlanBlock[]
    state: RunProjectionState
  }>()
  let observerReady: Promise<void> | null = null
  let runtimeTimer: ReturnType<typeof setInterval> | null = null

  // ---- subagent_* call cards -------------------------------------------
  // The live half of the cards: which card task an agent's current execution
  // belongs to, what it last did, and when each execution started and
  // stopped. The facts themselves ride on the steps; these bindings are what
  // only the event stream knows (plan 014 §2).
  const subagentCallTasks = reactive(new Map<string, Map<number, SubagentCallTaskLive>>())
  const currentCallTaskOfAgent = new Map<string, { stepId: string; index: number }>()
  // Step ids that belong to a subagent_* call — a lifecycle event binds to
  // its dispatching card only through them, never by guessing.
  const subagentCallStepIds = new Set<string>()
  // How an execution ended, so a query row whose result arrives after the
  // end still settles the way the terminal's live order settles it.
  const endedSubagentExecutions = new Map<string, { status: 'done' | 'failed' | 'cancelled'; finishedAtMs: number }>()
  // The query-card rows that reported a still-running execution, settled
  // when that execution ends.
  const queryRowsByExecution = new Map<string, { stepId: string; index: number }[]>()
  const subagentNow = ref(Date.now())
  let subagentClockTimer: ReturnType<typeof setInterval> | null = null

  function syncSubagentClock() {
    let anyRunning = false
    for (const tasks of subagentCallTasks.values()) {
      for (const task of tasks.values()) {
        if (task.status === 'running') {
          anyRunning = true
          break
        }
      }
      if (anyRunning) break
    }
    if (anyRunning && subagentClockTimer == null) {
      subagentClockTimer = setInterval(() => {
        subagentNow.value = Date.now()
      }, 1_000)
    } else if (!anyRunning && subagentClockTimer != null) {
      clearInterval(subagentClockTimer)
      subagentClockTimer = null
    }
  }

  function clearSubagentCardState() {
    subagentCallTasks.clear()
    currentCallTaskOfAgent.clear()
    subagentCallStepIds.clear()
    endedSubagentExecutions.clear()
    queryRowsByExecution.clear()
    syncSubagentClock()
  }

  function liveTasksOf(stepId: string): Map<number, SubagentCallTaskLive> {
    let tasks = subagentCallTasks.get(stepId)
    if (!tasks) {
      tasks = new Map()
      subagentCallTasks.set(stepId, tasks)
    }
    return tasks
  }

  /** rememberSubagentCallStep indexes a step that draws one of these cards. */
  function rememberSubagentCallStep(step: SubagentToolStep) {
    if (!step.subagentCall) return
    subagentCallStepIds.add(step.stepId)
    if (!SUBAGENT_QUERY_VERBS.has(step.subagentCall.verb)) return
    // A query card is the snapshot its call took. Its live half only follows
    // the executions the rows reported as running: when one ends in this
    // session, the row ends with it (TUI §9). A row the call could not name
    // yet takes the title the agent's current binding already knows.
    const settled = Boolean(step.status) && !['', 'running', 'pending', 'awaiting approval'].includes(step.status.toLowerCase())
    for (const task of step.subagentCall.tasks ?? []) {
      const key = String(task.key ?? '').trim()
      if (!key || task.status !== 'running') continue
      const executionId = String(task.executionId ?? '').trim()
      const index = Number(task.index)
      const bound = currentCallTaskOfAgent.get(key)
      const boundTask = bound ? subagentCallTasks.get(bound.stepId)?.get(bound.index) : undefined
      const tasks = liveTasksOf(step.stepId)
      if (!tasks.has(index)) {
        tasks.set(index, {
          agentId: key,
          executionId: executionId || undefined,
          title: String(task.title ?? '').trim() || boundTask?.title,
          agentType: String(task.agentType ?? '').trim() || boundTask?.agentType,
          status: 'running',
          startedAtMs: Number(task.startedAt) > 0 ? Number(task.startedAt) * 1000 : boundTask?.startedAtMs,
          toolCount: 0,
        })
        subagentCallTasks.set(step.stepId, tasks)
      }
      if (!settled || !executionId) continue
      // A replayed transcript can deliver a run's rows after its subagent
      // events; the row settles from that record now, exactly as the live
      // order settles from the event.
      const ended = endedSubagentExecutions.get(executionId)
      if (ended) {
        settleLiveTask(step.stepId, index, ended.status, ended.finishedAtMs, undefined)
        continue
      }
      const rows = queryRowsByExecution.get(executionId) ?? []
      if (!rows.some((row) => row.stepId === step.stepId && row.index === index)) {
        rows.push({ stepId: step.stepId, index })
      }
      queryRowsByExecution.set(executionId, rows)
    }
  }

  function settleLiveTask(stepId: string, index: number, status: 'done' | 'failed' | 'cancelled', finishedAtMs: number, error?: string) {
    const tasks = subagentCallTasks.get(stepId)
    const task = tasks?.get(index)
    if (!tasks || !task) return
    tasks.set(index, { ...task, status, finishedAtMs, error: error || task.error })
    subagentCallTasks.set(stepId, tasks)
  }

  /**
   * bindSubagentSpawn writes a spawn into the card task it belongs to: the
   * call its parent_tool_call_id names, else a card of its own keyed by the
   * execution. The task's clock starts at the spawn itself.
   */
  function bindSubagentSpawn(evt: ForebrainRunEvent, payload: Record<string, unknown>, assistantMessageId: string): void {
    const agentId = String(payload.agentId ?? '').trim()
    const agentType = String(payload.agentType ?? '').trim()
    // A goal's check belongs to the goal: it draws no card of its own.
    if (agentType === FOREBRAIN_GOAL_CHECK_AGENT_TYPE) return
    const title = String(payload.title ?? '').trim()
    const task = String(payload.task ?? '').trim()
    const executionId = String(payload.executionId ?? evt.runId ?? evt.id ?? '').trim()
    const startedAtMs = eventTimestamp(evt.createdAt)
    const parentToolCallId = String(payload.parentToolCallId ?? '').trim()
    const taskIndexRaw = Number(payload.taskIndex)
    const taskIndex = Number.isFinite(taskIndexRaw) && taskIndexRaw >= 0 ? taskIndexRaw : 0

    let stepId = parentToolCallId
    let index = taskIndex
    if (!stepId || !subagentCallStepIds.has(stepId)) {
      // No dispatching call is known: an execution dispatched with no call
      // opens a card of its own, keyed by the execution so the spawn's
      // re-delivery finds it.
      stepId = `subagent-exec:${executionId || agentId}`
      index = 0
      upsertSubagentExecutionBlock(evt, stepId, {
        stepId,
        toolName: '',
        summary: title || task || agentType,
        status: 'running',
        subagentCall: {
          verb: 'run',
          tasks: [{ index: 0, title: title || firstLine(task) || agentType, agentType, status: 'waiting' }],
        },
      }, assistantMessageId)
    }
    const tasks = liveTasksOf(stepId)
    const existing = tasks.get(index)
    // A task already settled by its own end keeps its outcome; the identity
    // is still the fact this message carries.
    tasks.set(index, {
      agentId,
      executionId: executionId || undefined,
      title: title || existing?.title,
      agentType: agentType || existing?.agentType,
      status: existing && subagentTaskTerminal(String(existing.status ?? '')) ? existing.status : 'running',
      startedAtMs: existing?.startedAtMs ?? startedAtMs,
      toolCount: existing?.toolCount ?? 0,
      latestTool: existing?.latestTool,
      toolStepIds: existing?.toolStepIds,
    })
    subagentCallTasks.set(stepId, tasks)
    subagentCallStepIds.add(stepId)
    if (agentId) currentCallTaskOfAgent.set(agentId, { stepId, index })
    syncSubagentClock()
  }

  /**
   * settleSubagentEnd writes an execution's outcome into the card task it was
   * bound to — the one its dispatching call names, else the one this agent's
   * execution was bound to — and closes the query rows that reported it.
   */
  function settleSubagentEnd(evt: ForebrainRunEvent, payload: Record<string, unknown>, assistantMessageId: string): void {
    const agentId = String(payload.agentId ?? '').trim()
    const errText = String(payload.error ?? '').trim()
    // A goal's check never opened a card; its end closes none.
    if (String(payload.agentType ?? '').trim() === FOREBRAIN_GOAL_CHECK_AGENT_TYPE) return
    const status = subagentEndStatus(String(payload.status ?? '').trim().toLowerCase(), errText)
    const finishedAtRaw = Number(payload.finishedAtMs)
    const finishedAtMs = Number.isFinite(finishedAtRaw) && finishedAtRaw > 0 ? finishedAtRaw : eventTimestamp(evt.createdAt)
    const parentToolCallId = String(payload.parentToolCallId ?? '').trim()
    const taskIndexRaw = Number(payload.taskIndex)
    const taskIndex = Number.isFinite(taskIndexRaw) && taskIndexRaw >= 0 ? taskIndexRaw : 0
    const executionId = String(payload.executionId ?? evt.runId ?? '').trim()

    let stepId = parentToolCallId && subagentCallStepIds.has(parentToolCallId) ? parentToolCallId : ''
    let index = taskIndex
    if (!stepId && agentId) {
      const bound = currentCallTaskOfAgent.get(agentId)
      if (bound) {
        stepId = bound.stepId
        index = bound.index
      }
    }
    if (stepId) {
      settleLiveTask(stepId, index, status, finishedAtMs, errText || undefined)
    } else {
      // An end whose start was never seen closes a card of its own, already
      // ended — the terminal's rule for a replay window that begins
      // mid-execution.
      stepId = `subagent-exec:${executionId || agentId}`
      upsertSubagentExecutionBlock(evt, stepId, {
        stepId,
        toolName: '',
        summary: String(payload.agentType ?? '').trim() || agentId,
        status: 'completed',
        subagentCall: {
          verb: 'run',
          tasks: [{ index: 0, key: agentId, agentType: String(payload.agentType ?? '').trim(), status, error: errText || undefined, executionId }],
        },
      }, assistantMessageId)
      const tasks = liveTasksOf(stepId)
      tasks.set(0, { agentId, executionId: executionId || undefined, status, error: errText || undefined, finishedAtMs })
      subagentCallTasks.set(stepId, tasks)
      subagentCallStepIds.add(stepId)
    }
    currentCallTaskOfAgent.delete(agentId)
    const executionKey = executionId || agentId
    if (executionKey) {
      endedSubagentExecutions.set(executionKey, { status, finishedAtMs })
      const rows = queryRowsByExecution.get(executionKey)
      if (rows) {
        for (const row of rows) settleLiveTask(row.stepId, row.index, status, finishedAtMs, errText || undefined)
        queryRowsByExecution.delete(executionKey)
      }
    }
    syncSubagentClock()
  }

  /**
   * countSubagentTool folds one tool event of a subagent into its card task:
   * the count rises only on a start, a completion only refines the label —
   * the terminal's appendFanoutTaskTool rule.
   */
  function countSubagentTool(agentId: string, stepId: string, label: string, started: boolean) {
    const bound = currentCallTaskOfAgent.get(agentId)
    if (!bound) return
    const tasks = subagentCallTasks.get(bound.stepId)
    const task = tasks?.get(bound.index)
    if (!tasks || !task) return
    const seen = task.toolStepIds ?? []
    const known = seen.includes(stepId)
    let toolCount = task.toolCount ?? 0
    if (!known && started) {
      seen.push(stepId)
      toolCount += 1
    } else if (!known && !started) {
      return
    }
    tasks.set(bound.index, { ...task, toolCount, latestTool: label || task.latestTool, toolStepIds: seen })
    subagentCallTasks.set(bound.stepId, tasks)
  }

  /**
   * upsertSubagentExecutionBlock places one "no dispatching call" card: in
   * the transcript of the subagent whose execution is the event's parent
   * run, else in the timeline the event was already projected onto — never
   * a new, empty message in the main conversation (W7). The block id and
   * the card key (the step's stepId) are separate: a plan review's card is
   * keyed by its review id, which is also what its reviewer's spawned event
   * names.
   */
  function upsertSubagentExecutionBlock(evt: ForebrainRunEvent, blockId: string, step: SubagentToolStep, fallbackAssistantMessageId?: string) {
    const block: Extract<TimelineBlock, { kind: 'subagent' }> = { kind: 'subagent', id: blockId, stepId: step.stepId, step }
    const payload = evt.payload ?? {}
    const parentRunId = String((payload as Record<string, unknown>).parentRunId ?? '').trim()
    const subagentHost = parentRunId
      ? subagents.value.find((entry) => Boolean(entry.executionStarts && parentRunId in entry.executionStarts))
      : undefined
    if (subagentHost) {
      updateSubagent(subagentHost.agentId, (entry) => (
        entry.blocks.some((existing) => existing.kind === 'subagent' && existing.id === blockId)
          ? entry
          : { ...entry, blocks: [...entry.blocks, block] }
      ))
      return
    }
    const hostId = fallbackAssistantMessageId || eventProjection(evt).assistantMessageId
    if (!hostId) return
    updateMessageById(hostId, (message) => (
      (message.blocks ?? []).some((existing) => existing.kind === 'subagent' && existing.id === blockId)
        ? message
        : { ...message, blocks: [...(message.blocks ?? []), block] }
    ))
  }

  function stopRuntimeTimer() {
    if (runtimeTimer) {
      clearInterval(runtimeTimer)
      runtimeTimer = null
    }
  }

  function startRuntimeStatus(kind: RuntimeStatusKind, startedAt = Date.now(), attempt?: number) {
    stopRuntimeTimer()
    runtimeStatus.value = { kind, startedAt, elapsedMs: Date.now() - startedAt, attempt, maxAttempts: 5 }
    runtimeTimer = setInterval(() => {
      runtimeStatus.value = {
        ...runtimeStatus.value,
        elapsedMs: Date.now() - startedAt,
      }
    }, 1000)
  }

  function updateRuntimeKind(kind: RuntimeStatusKind, attempt?: number) {
    const startedAt = runtimeStatus.value.startedAt ?? Date.now()
    runtimeStatus.value = {
      ...runtimeStatus.value,
      kind,
      startedAt,
      elapsedMs: Date.now() - startedAt,
      ...(attempt != null ? { attempt, maxAttempts: 5 } : {}),
    }
  }

  /** The working line says when a goal's check is reading the workspace. */
  function setGoalChecking(checking: boolean) {
    if (runtimeStatus.value.kind === 'idle') return
    runtimeStatus.value = { ...runtimeStatus.value, goalChecking: checking }
  }

  function clearRuntimeStatus() {
    stopRuntimeTimer()
    runtimeStatus.value = { kind: 'idle', elapsedMs: 0 }
  }

  function eventProjection(evt: ForebrainRunEvent) {
	const payload = evt.payload ?? {}
	const childOnly = evt.type !== 'subagent_spawned' && evt.type !== 'subagent_ended' && isSubagentHistoryEvent(evt)
	if (childOnly) {
	  // Child transcript events return from handleRunEvent before touching a
	  // primary message. Giving them a scratch projection avoids inventing an
	  // empty primary answer when a partial/legacy child history has no spawn.
	  return {
		assistantMessageId: '',
		planBlocks: [] as PlanBlock[],
		state: { fullAnswer: '' } as RunProjectionState,
	  }
	}
	const parentRunId = (evt.type === 'subagent_spawned' || evt.type === 'subagent_ended')
	  ? String(payload.parentRunId ?? '').trim()
	  : ''
	// A compaction outside any run — the one the user asked for, or the one
	// before a turn — is a card of its own where it happened, not part of
	// whatever other runless event came first.
	const compactionId = FOREBRAIN_COMPACTION_EVENT_TYPES.has(String(evt.type)) ? String(payload.compactionId ?? '').trim() : ''
	// A compaction whose card is already drawn keeps it: the socket that
	// delivers its next event is not a reason to draw a second one.
	const existingHost = compactionId ? compactionHostId(compactionId) : undefined
	if (existingHost) {
	  return {
		assistantMessageId: existingHost,
		planBlocks: [] as PlanBlock[],
		state: { fullAnswer: '' } as RunProjectionState,
	  }
	}
	const runId = parentRunId || String(evt.runId ?? '').trim() || (compactionId ? compactionBlockId(compactionId) : 'session')
    const known = eventProjectionByRun.get(runId)
    if (known) return known
	let host = [...messages.value].reverse().find((message) => message.role === 'assistant' && String(message.runId ?? '').trim() === runId)
	// The legacy ledger has no durable parent-run association, so its best
	// effort fallback remains the latest assistant. Canonical events must never
	// use that guess: a turn with no parent answer still owns its subagent cards
	// and needs a distinct, empty assistant host after resume.
	if (!host && evt.type === 'legacy_subagent') {
	  host = [...messages.value].reverse().find((message) => message.role === 'assistant')
	}
    if (!host) {
      host = {
		id: `assistant-run-events-${runId}`,
		role: 'assistant',
		content: '',
		runId,
	  }
      messages.value = [...messages.value, host]
    }
    const projection = {
      assistantMessageId: String(host.id),
      planBlocks: [] as PlanBlock[],
      state: { fullAnswer: '' } as RunProjectionState,
    }
    eventProjectionByRun.set(runId, projection)
    return projection
  }

  /**
   * The canonical event types the request socket's legacy operations already
   * project, one per operation: run_started, run_completed, run_cancelled,
   * run_error, mode_changed, pending_input_updated. The answer itself has no
   * legacy operation: it streams only as assistant_delta events. While a turn
   * streams on that socket these arrive twice - once as the legacy operation and
   * once as the canonical event - and only one of them may be applied.
   *
   * Every other type has no legacy operation behind it. Suppressing the whole
   * run, as this once did, therefore dropped the conversation's tool cards, its
   * approvals and its plan updates for the entire turn: they existed only in the
   * canonical stream, and nothing was listening to it. They reappeared on the
   * next reload, which is exactly the drift between a live turn and a reloaded
   * one this set exists to prevent.
   */
  const legacyMirroredEventTypes = new Set([
    'turn_started',
    'turn_completed',
    'turn_cancelled',
    'turn_error',
    'mode_changed',
    'pending_input_updated',
  ])

  /**
   * Conversation events the transcript already carries. A reload rebuilds the
   * turn from its own rows - what it said, what it thought, the calls it made -
   * so replaying these on top of that says everything twice. What the rows
   * cannot express keeps coming from the log: the line an approval printed, a
   * plan update, a turn's diff, the run's timing.
   *
   * A subagent has no rows of its own and is rebuilt from these events alone,
   * which is why the rule is scoped to the conversation.
   */
  const transcriptBackedEventTypes = new Set([
    'assistant_delta',
    'reasoning_delta',
    'reasoning_done',
    // A delivered steer is a user row of the transcript once its run is persisted.
    'input_delivered',
    'tool_call_started',
    'tool_call_completed',
    'tool_output_delta',
  ])

  function legacyAlreadyProjected(evt: ForebrainRunEvent): boolean {
    if (isSubagentHistoryEvent(evt)) return false
    if (!legacyMirroredRunIDs.has(String(evt.runId ?? ''))) return false
    return legacyMirroredEventTypes.has(String(evt.type))
  }

  function isSubagentHistoryEvent(evt: ForebrainRunEvent): boolean {
    if (evt.type === 'subagent_spawned' || evt.type === 'subagent_ended') return true
    return Boolean(subagentIdFromPayload(evt.payload ?? {}))
  }

  function rememberObservedEvent(evt: ForebrainRunEvent): boolean {
    const eventId = String(evt.id ?? '').trim()
    if (eventId && appliedEventIDs.has(eventId)) return false
    if (eventId) appliedEventIDs.add(eventId)
    sessionEventCursor = Math.max(sessionEventCursor, Number(evt.sequence ?? 0))
    return true
  }

  /**
   * applyAutoContinueEvent applies the auto-continue lifecycle, which belongs
   * to the conversation rather than to any run's timeline: its events are filed
   * under the run the limit stopped, and projecting them there would reopen a
   * finished turn.
   *
   * Only events after the observer bound say anything: the binding's snapshot
   * already states what is pending, and a scheduled event replayed from before
   * a gateway restart describes a wait that no longer exists. The replay also
   * overlaps the history rows, which already hold the continuation's message
   * as the user row the runtime wrote, so it is drawn only when it is new.
   */
  function applyAutoContinueEvent(evt: ForebrainRunEvent, historical: boolean) {
    const sequence = Number(evt.sequence ?? 0)
    if (historical || (sequence > 0 && sequence <= observerHighWater)) return
    const payload = (evt.payload ?? {}) as Record<string, unknown>
    autoContinueSequence = Math.max(autoContinueSequence, sequence)
    const agentId = String(payload.agentId ?? '').trim()
    switch (evt.type) {
      case 'auto_continue_scheduled':
        setAutoContinueFor(agentId, parseAutoContinue(payload))
        return
      case 'auto_continue_cancelled':
        setAutoContinueFor(agentId, null)
        return
      case 'auto_continue_started': {
        setAutoContinueFor(agentId, null)
        const prompt = String(payload.prompt ?? '').trim()
        // The conversation's own continuation draws its message here; a
        // subagent's reaches that subagent's view from the run's own rows.
        if (prompt && !agentId) {
          messages.value = [...messages.value, {
            id: `auto-continue-${String(evt.id ?? '').trim() || Date.now()}`,
            role: 'user',
            content: prompt,
          }]
        }
        return
      }
    }
  }

  /**
   * setAutoContinueFor stores one view's wait: the conversation's own on its
   * ref, a subagent's in the per-agent map (null removes it).
   */
  function setAutoContinueFor(agentId: string, state: AutoContinueState | null) {
    const key = String(agentId ?? '').trim()
    if (!key) {
      autoContinue.value = state
      return
    }
    const next = { ...subagentAutoContinue.value }
    if (state) next[key] = state
    else delete next[key]
    subagentAutoContinue.value = next
  }

  /**
   * applyBoundAutoContinues restores what a freshly bound page should show:
   * the conversation's own wait and each subagent's, from the binding's list
   * (falling back to the single auto_continue an older server sends).
   */
  function applyBoundAutoContinues(bound: { autoContinue?: AutoContinueState; autoContinues?: AutoContinueEntry[] }) {
    const next: Record<string, AutoContinueState> = {}
    let primary: AutoContinueState | null = null
    for (const entry of bound.autoContinues ?? []) {
      const { agentId, ...state } = entry
      const key = String(agentId ?? '').trim()
      if (!key) primary = state
      else next[key] = state
    }
    if (!primary && bound.autoContinue) primary = bound.autoContinue
    autoContinue.value = primary
    subagentAutoContinue.value = next
  }

  /**
   * applyHeartbeatFiredEvent draws the message a heartbeat sent as the first
   * row of the turn it started, the way a reload will show it once the history
   * rows hold it. The event is filed under the run the heartbeat began, so
   * projecting it there would draw a user turn inside the timeline of a run
   * that has not answered yet.
   *
   * Only events after the observer bound say anything: the replay overlaps the
   * history rows, which already carry the prompt as the user row the runtime
   * wrote, so it is drawn only when it is new.
   */
  function applyHeartbeatFiredEvent(evt: ForebrainRunEvent, historical: boolean) {
    const sequence = Number(evt.sequence ?? 0)
    if (historical || (sequence > 0 && sequence <= observerHighWater)) return
    const payload = (evt.payload ?? {}) as Record<string, unknown>
    const prompt = String(payload.prompt ?? '').trim()
    if (!prompt) return
    messages.value = [...messages.value, {
      id: `heartbeat-${String(evt.id ?? '').trim() || Date.now()}`,
      role: 'user',
      content: prompt,
      origin: 'heartbeat',
    }]
  }

  function applyObservedEvent(evt: ForebrainRunEvent, historical = false) {
    const sid = String(evt.sessionId ?? '').trim()
    if (sid && sid !== String(sessionId.value ?? '').trim()) return
    if (AUTO_CONTINUE_EVENT_TYPES.has(String(evt.type))) {
      if (rememberObservedEvent(evt)) applyAutoContinueEvent(evt, historical)
      return
    }
    if (String(evt.type) === 'heartbeat_fired') {
      if (rememberObservedEvent(evt)) applyHeartbeatFiredEvent(evt, historical)
      return
    }
    // A language-server recommendation is asked once, live: history replays
    // the answer's transcript line, and an event the observer already
    // accounted for is a reconnect, not a second offer.
    if (String(evt.type) === LSP_RECOMMENDATION_EVENT_TYPE) {
      const sequence = Number(evt.sequence ?? 0)
      if (rememberObservedEvent(evt) && !historical && !(sequence > 0 && sequence <= observerHighWater)) {
        lspRecommendation.value = parseLspRecommendation(evt.payload)
      }
      return
    }
    // The request websocket still carries legacy primary-turn operations for
    // compatibility, and their canonical mirror is observed here too. Record
    // that mirror's cursor/id rather than projecting it twice - but only for the
    // types those operations actually cover; everything else reaches the
    // conversation through this path alone.
    if (!historical && legacyAlreadyProjected(evt)) {
      rememberObservedEvent(evt)
      return
    }
    if (historical && !isSubagentHistoryEvent(evt) && transcriptBackedEventTypes.has(String(evt.type))) {
      rememberObservedEvent(evt)
      return
    }
    // A reload draws each finished compaction from its end alone: the history
    // rows carry the conversation's own, and a subagent's is rebuilt from the
    // event that ended it. What only drove the running card replays nothing,
    // so a compaction interrupted before it ended never reloads as running.
    if (historical && FOREBRAIN_COMPACTION_EVENT_TYPES.has(String(evt.type)) &&
      (!isSubagentHistoryEvent(evt) || evt.type === 'context_compacting' || evt.type === 'context_compact_progress')) {
      rememberObservedEvent(evt)
      return
    }
    // A goal's lines come with the history rows, where they were drawn live.
    if (historical && FOREBRAIN_GOAL_EVENT_TYPES.has(String(evt.type))) {
      rememberObservedEvent(evt)
      return
    }
    if (!historical && (historyLoadingSession || isStreaming.value)) {
      bufferedSessionEvents.push(evt)
      return
    }
    const projection = eventProjection(evt)
    handleRunEvent(evt, projection.assistantMessageId, projection.planBlocks, projection.state)
  }

  function flushBufferedSessionEvents() {
    if (historyLoadingSession || isStreaming.value || bufferedSessionEvents.length === 0) return
    const queued = bufferedSessionEvents.splice(0, bufferedSessionEvents.length)
      .sort((a, b) => Number(a.sequence ?? 0) - Number(b.sequence ?? 0))
    for (const evt of queued) applyObservedEvent(evt)
  }

  function startSessionObserver(sid: string): Promise<void> {
    const target = sid.trim()
    if (!target) return Promise.resolve()
	if (typeof WebSocket === 'undefined') return Promise.resolve()
    if (sessionObserver && observerSessionId === target && observerReady) return observerReady
    if (sessionObserver) {
      try { sessionObserver.close() } catch { /* noop */ }
    }
    observerSessionId = target
    const ws = new WebSocket(buildBrowserForebrainGatewayChatWsUrl())
    sessionObserver = ws
    observerReady = new Promise<void>((resolve) => {
      let settled = false
      const ready = () => {
        if (!settled) {
          settled = true
          resolve()
        }
      }
      ws.onopen = () => {
        ws.send(JSON.stringify({
          op: 'bind_session',
		  protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
          request_id: `observe-${Date.now()}`,
          session_id: target,
          cursor: sessionEventCursor,
        }))
      }
      ws.onmessage = (message) => {
        try {
          const raw = JSON.parse(String(message.data)) as Record<string, unknown>
		  const hello = parseForebrainGatewayServerHello(raw)
		  if (hello?.protocolVersion && hello.protocolVersion !== FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION) {
			observerSessionId = ''
			setError(`Gateway protocol mismatch (client ${FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION}, server ${hello.protocolVersion})`)
			try { ws.close() } catch { /* noop */ }
			ready()
			return
		  }
          const bound = parseForebrainSessionBoundMessage(raw)
          if (bound) {
            observerHighWater = bound.highWater ?? 0
            if (autoContinueSequence <= observerHighWater) applyBoundAutoContinues(bound)
            ready()
            return
          }
          const mcpStatusMsg = parseForebrainMcpStatusMessage(raw)
          if (mcpStatusMsg) {
            mcpStatus.value = mcpStatusMsg
            return
          }
          const evt = parseForebrainRunEventMessage(raw)
		  if (evt && !isSupportedForebrainRunEventSchema(evt.schemaVersion)) {
			observerSessionId = ''
			setError(`Unsupported session event schema ${evt.schemaVersion}; this client supports ${FOREBRAIN_RUN_EVENT_SCHEMA_VERSION}`)
			try { ws.close() } catch { /* noop */ }
			ready()
			return
		  }
          if (evt) applyObservedEvent(evt)
        } catch {
          // A malformed optional notification must not tear down observation.
        }
      }
      ws.onerror = ready
      ws.onclose = () => {
        ready()
        if (sessionObserver === ws) sessionObserver = null
        if (observerSessionId === target && sessionId.value === target) {
          setTimeout(() => {
            if (!sessionObserver && observerSessionId === target && sessionId.value === target) {
              observerReady = null
              void startSessionObserver(target)
            }
          }, 500)
        }
      }
    })
    return observerReady
  }

  function applySessionReset(options: SessionResetOptions) {
    const sid = String(options.sid ?? '').trim()
    sessionId.value = sid || null
	  historyGeneration += 1
	  sessionEventCursor = 0
	  appliedEventIDs.clear()
	  legacyMirroredRunIDs.clear()
	  eventProjectionByRun.clear()
	  clearSubagentCardState()
	  bufferedSessionEvents.splice(0, bufferedSessionEvents.length)
	  historyLoadingSession = sid
	  if (sessionObserver) {
	    try { sessionObserver.close() } catch { /* noop */ }
	    sessionObserver = null
	  }
	  observerSessionId = ''
	  observerHighWater = 0
	  autoContinueSequence = 0
	  autoContinue.value = null
	  subagentAutoContinue.value = {}
	  lspRecommendation.value = null
    const generation = historyGeneration
    persistLastSessionId(sid || null)
    messages.value = []
	historyLoading.value = Boolean(sid && options.loadMessages)
	historyError.value = null
    plan.value = null
    subagents.value = []
    answer.value = ''
    setError(null)
    serverPendingInput.value = emptyPendingInputPreview()
    // Messages held for a run in the conversation being left belong to no
    // conversation now; they go back to the composer rather than follow it.
    const held = heldBeforeRun.value
    heldBeforeRun.value = []
    giveBackHeld(held)
    isStreaming.value = false
    clearRuntimeStatus()
    streamAbortController = null
    activeRunId = null
    contextSignals.value = emptyContextRuntimeSignals()

    if (!sid) {
      mode.value = 'agent'
      modePhase.value = ''
      return
    }
    if (options.loadMessages) {
      loadMessages(sid).catch(() => {})
	} else {
	  historyLoadingSession = ''
    }
	void startSessionObserver(sid)
    if (options.loadMode) {
      forebrainApi.sessionMode(sid).then((m) => {
        if (generation !== historyGeneration || sessionId.value !== sid) return
        mode.value = normalizeMode(m.mode)
        modePhase.value = String(m.phase ?? '').trim()
      }).catch(() => {
        if (generation !== historyGeneration || sessionId.value !== sid) return
        mode.value = 'agent'
        modePhase.value = ''
      })
    }
  }

  function updateSubagent(agentId: string, apply: (entry: SubagentTranscript) => SubagentTranscript) {
    const id = agentId.trim()
    if (!id) return
    const index = subagents.value.findIndex((entry) => entry.agentId === id)
    const current = index >= 0 ? subagents.value[index] : emptySubagentTranscript(id)
    const next = { ...apply({ ...current }), updatedSeq: current.updatedSeq + 1 }
    subagents.value = index >= 0
      ? subagents.value.map((entry, i) => (i === index ? next : entry))
      : [...subagents.value, next]
  }

  /**
   * routeSubagentEvent keeps everything a subagent produces on the subagent's
   * own transcript. Without it the child's answer would be appended to the
   * primary assistant message and its tool calls mixed into the main step list,
   * so the conversation would read as if the agent had said things it never
   * said.
   */
  function routeSubagentEvent(agentId: string, evt: ForebrainRunEvent, payload: Record<string, unknown>): boolean {
    switch (evt.type) {
      case 'context_compacting':
      case 'context_compact_progress':
      case 'context_compacted':
      case 'context_compact_failed': {
        // A subagent's compaction rewrote that subagent's history; its card is
        // part of the subagent's own transcript.
        const update = parseCompactionEvent(evt.type, payload)
        if (update) {
          updateSubagent(agentId, (entry) => ({ ...entry, blocks: applyCompactionUpdate(entry.blocks, update, evt.createdAt) }))
        }
        return true
      }
      case 'assistant_delta': {
        const text = String(payload.text ?? '')
        if (!text) return true
        updateSubagent(agentId, (entry) => ({
          ...entry,
          // Text after thinking closes the thinking block, the way the terminal
          // finalises reasoning before it prints the answer.
          blocks: appendStreamedText(
            closeStreamedBlocks(entry.blocks, ['thinking'], evt.createdAt),
            'assistant',
            text,
            evt.id,
            evt.createdAt,
          ),
        }))
        return true
      }
      case 'reasoning_delta': {
        const text = String(payload.text ?? '')
        if (!text) return true
        updateSubagent(agentId, (entry) => ({
          ...entry,
          // Thinking after text closes the text block, the way text closes
          // the thinking block: a block of one kind ends when a block of the
          // other starts, in either direction.
          blocks: appendStreamedText(
            closeStreamedBlocks(entry.blocks, ['assistant'], evt.createdAt),
            'thinking',
            text,
            evt.id,
            evt.createdAt,
          ),
        }))
        return true
      }
      case 'reasoning_done':
        updateSubagent(agentId, (entry) => ({
          ...entry,
          blocks: closeStreamedBlocks(entry.blocks, ['thinking'], evt.createdAt),
        }))
        return true
      case 'usage_delta':
        updateSubagent(agentId, (entry) => ({
          ...entry,
          inputTokens: entry.inputTokens + Number(payload.inputTokens ?? 0),
          outputTokens: entry.outputTokens + Number(payload.outputTokens ?? 0),
        }))
        return true
      case 'tool_call_started':
      case 'tool_call_completed': {
        const completed = evt.type === 'tool_call_completed'
        const step = toolStepFromPayload(payload, completed, `${payload.toolName ?? 'tool'}-${evt.id ?? ''}`)
        // A subagent's own call is one of these cards when it says so, and
        // every tool event it runs lands on the card task it was bound to.
        rememberSubagentCallStep(step)
        countSubagentTool(
          agentId,
          step.stepId,
          String(subagentStepMeta(payload).invocation ?? '').trim() || step.toolName,
          !completed,
        )
        updateSubagent(agentId, (entry) => {
          entry = { ...entry, blocks: closeStreamedBlocks(entry.blocks, ['assistant', 'thinking'], evt.createdAt) }
          const index = entry.blocks.findIndex((b) => b.kind === 'tool' && b.step.stepId === step.stepId)
          if (index >= 0) {
            // The completion replaces the running card in place rather than
            // adding a second one for the same call.
            const blocks = entry.blocks.map((block, i) => (
              i === index && block.kind === 'tool'
                ? { kind: 'tool' as const, step: { ...block.step, ...step } }
                : block
            ))
            return { ...entry, blocks }
          }
          // A call interrupts whatever the subagent was saying, so the text so
          // far becomes its own block above the call.
          const closed = closeStreamedBlocks(entry.blocks, ['assistant', 'thinking'], evt.createdAt)
          return { ...entry, blocks: [...closed, { kind: 'tool', step }] }
        })
        return true
      }
      case 'plan_updated': {
        // A subagent keeps its own todo list. Leaving this to the main handler
        // put a worker's checklist in the conversation and overwrote the
        // conversation's own plan progress with it.
        const plan = parsePlanUpdatePayload(payload)
        if (!plan) return true
        updateSubagent(agentId, (entry) => ({
          ...entry,
          // The facts its working line shows and its next worked line closes
          // with — the payload's own active task, not its explanation.
          plan: (plan.total ?? 0) > 0
            ? { done: plan.completed ?? 0, total: plan.total ?? 0, active: plan.active }
            : undefined,
          blocks: [
            ...closeStreamedBlocks(entry.blocks, ['assistant', 'thinking'], evt.createdAt),
            { kind: 'plan' as const, id: evt.id, plan },
          ],
        }))
        return true
      }
      case 'tool_output_delta': {
        const stepId = String(payload.stepId ?? '').trim()
        const text = String(payload.text ?? '')
        if (!stepId || !text) return true
        updateSubagent(agentId, (entry) => ({
          ...entry,
          blocks: entry.blocks.map((block) => (
            block.kind === 'tool' && block.step.stepId === stepId
              ? { ...block, step: { ...block.step, output: `${block.step.output ?? ''}${text}` } }
              : block
          )),
        }))
        return true
      }
      case 'approval_requested': {
        const actionId = String(payload.actionId ?? '').trim()
        updateSubagent(agentId, (entry) => ({
          ...entry,
          status: 'waiting_approval',
          blocks: actionId && !entry.blocks.some((block) => block.kind === 'approval' && block.actionId === actionId)
            ? [...closeStreamedBlocks(entry.blocks, ['assistant', 'thinking'], evt.createdAt), {
                kind: 'approval' as const,
                id: evt.id ?? `approval-${actionId}`,
                actionId,
                actionKind: String(payload.actionKind ?? '').trim(),
                status: 'pending',
                message: String(payload.message ?? '').trim() || undefined,
              }]
            : entry.blocks,
        }))
        return true
      }
      case 'approval_resolved': {
        const actionId = String(payload.actionId ?? '').trim()
	const decision = normalizeApprovalDecision(String(payload.decision ?? payload.status ?? 'resolved'))
        // A delivered review's record carries the internal handoff key, not
        // user-facing words: the line is dropped, the decision still lands.
        const confirmation = (() => {
          const raw = String(payload.confirmation ?? '').trim()
          return raw && raw !== PLAN_REVIEW_DELIVERED_KEY ? raw : undefined
        })()
        updateSubagent(agentId, (entry) => ({
          ...entry,
		  status: decision === 'cancelled'
			? 'cancelled'
			: decision === 'expired' || decision === 'error'
			  ? 'failed'
			: entry.status === 'waiting_approval' ? 'running' : entry.status,
		  blocks: entry.blocks.some((block) => block.kind === 'approval' && block.actionId === actionId)
			? entry.blocks.map((block) => (
				block.kind === 'approval' && block.actionId === actionId
				  ? {
					  ...block,
					  status: decision,
					  confirmation: confirmation ?? block.confirmation,
					  message: String(payload.reason ?? block.message ?? '').trim() || undefined,
					}
				  : block
			  ))
			: actionId ? [...closeStreamedBlocks(entry.blocks, ['assistant', 'thinking'], evt.createdAt), {
				kind: 'approval' as const,
				id: evt.id ?? `approval-${actionId}`,
				actionId,
				actionKind: String(payload.actionKind ?? '').trim(),
				status: decision,
				confirmation,
				message: String(payload.reason ?? '').trim() || undefined,
			  }] : entry.blocks,
        }))
        return true
      }
      case 'pending_input_updated':
        // The subagent's own queue, as its own view shows it — never the
        // conversation's.
        applyPendingInputPreview(payload, agentId)
        return true
      case 'token_budget_updated': {
        const budget = parseTokenBudgetPayload(payload)
        if (budget) updateSubagent(agentId, (entry) => ({ ...entry, tokenBudget: budget }))
        return true
      }
      case 'subagent_input_delivered': {
        // A steer the runtime handed this subagent is the user's own message in
        // its transcript, the way the terminal draws it.
        const text = String(payload.text ?? '').trim()
        if (text) {
          updateSubagent(agentId, (entry) => ({
            ...entry,
            blocks: [...closeStreamedBlocks(entry.blocks, ['assistant', 'thinking'], evt.createdAt), { kind: 'user' as const, id: evt.id, text }],
          }))
        }
        return true
      }
      case 'queued_input_released': {
        // The subagent's execution ended and its queue decided what follows:
        // the next messages are sent to it, the rest go back to its composer.
        const next = releasedNextInputs(payload)
        const back = releasedInputs(payload)
        if (back.length) {
          void Promise.all(back.map(releasedSubmission)).then((subs) => {
            for (const sub of subs) returnSubagentDraft(agentId, sub)
          })
        }
        if (next.length) {
          void Promise.all(next.map(releasedSubmission)).then((subs) => {
            const merged = subs.reduce((acc, sub) => (acc ? mergeSubmissions(acc, sub) : sub), null as ComposerSubmission | null)
            if (merged) void sendToSubagent(agentId, merged, false)
          })
        }
        return true
      }
      default:
        return false
    }
  }

  function applyPlanRuntimeStatus(planUpdate: PlanUpdateData) {
    runtimeStatus.value = {
      ...runtimeStatus.value,
      planCompleted: planUpdate.completed,
      planTotal: planUpdate.total,
      // The task in flight is the payload's own derivation, not its
      // explanation line — the same title the terminal's working line names.
      planActive: planUpdate.active,
    }
  }

  function applyPendingInputPreview(raw: unknown, agentId = '') {
    const parsed = parsePendingInputPreview(raw)
    const id = String(agentId ?? '').trim()
    if (id) {
      // A subagent's queue belongs to its own view: an empty preview retires
      // the entry rather than keeping an empty one around under its key.
      if (parsed.pendingSteers.length || parsed.rejectedSteers.length || parsed.queuedMessages.length) {
        subagentPendingInput.value = { ...subagentPendingInput.value, [id]: parsed }
      } else if (subagentPendingInput.value[id]) {
        const next = { ...subagentPendingInput.value }
        delete next[id]
        subagentPendingInput.value = next
      }
      return
    }
    serverPendingInput.value = parsed
  }

  /**
   * Hands a message to a run that is still going: as a steer, or queued for
   * the turn after it. Any message steers whatever it attached (plan 004's
   * D7, the terminal's rule): what it carries travels in the queue's record
   * of it, so it can still be handed back whole. Reports whether the run
   * took the message; one that has just ended takes nothing, and the caller
   * gives the message back rather than lose it.
   */
  async function queueActiveRunInput(runId: string, userMessage: string, disposition: 'steer' | 'queue', attached?: SendOptions['attached']): Promise<boolean> {
    const text = String(userMessage ?? '').trim()
    const attachmentIds = (attached?.attachments ?? []).map((attachment) => attachment.fileId)
    const imagePaths = attached?.mentionImages ?? []
    if (disposition === 'steer') {
      try {
        const steered = await forebrainApi.runInput(runId, {
          message: text,
          attachments: attachmentIds.length ? attachmentIds : undefined,
          mentionImages: imagePaths.length ? imagePaths : undefined,
        })
        applyPendingInputPreview(steered.preview)
        if (steered.accepted) return true
      } catch {
        // A steer the run cannot take waits for the next turn instead.
      }
    }
    try {
      const queued = await forebrainApi.runQueuedInput(runId, {
        message: text,
        attachments: attachmentIds.length ? attachmentIds : undefined,
        mentionImages: imagePaths.length ? imagePaths : undefined,
      })
      applyPendingInputPreview(queued.preview)
      return queued.accepted
    } catch {
      return false
    }
  }

  /**
   * Moves the messages held while a run was being prepared into that run, in
   * the order they were sent. Any the run no longer takes come back to the
   * composer together, on the first of them.
   */
  async function flushHeldIntoRun(runId: string) {
    const held = heldBeforeRun.value
    if (!held.length) return
    heldBeforeRun.value = []
    for (const item of held) {
      const disposition = item.options.activeInputDisposition === 'queue' ? 'queue' : 'steer'
      if (!await queueActiveRunInput(runId, item.text, disposition, item.options.attached)) {
        giveBack(submissionOf(item.text, item.options))
      }
      item.settle()
    }
  }

  /** Gives messages that waited on a send back to the composer, in order. */
  function giveBackHeld(held: HeldSend[]) {
    for (const item of held) {
      giveBack(submissionOf(item.text, item.options))
      item.settle()
    }
  }

  /**
   * Gives a message back to the composer. Everything given back before the
   * composer takes it is one draft, in the order it was written. A refusal
   * travels as the runtime's sentence plus, when it sent one, the stable code
   * behind it — the code is what lets the notice say the sentence in the
   * viewer's language when it is drawn, the way a run's error block does.
   */
  function giveBack(submission: ComposerSubmission, notSentBecause = '', notSentBecauseCode = '') {
    if (notSentBecause) {
      returnedNotice.value = notSentBecause
      returnedNoticeCode.value = notSentBecauseCode.trim() || null
    }
    returnedDraft.value = returnedDraft.value ? mergeSubmissions(returnedDraft.value, submission) : submission
  }

  /** The composer takes what was given back to it. */
  function takeReturnedDraft(): ComposerSubmission | null {
    const draft = returnedDraft.value
    returnedDraft.value = null
    return draft
  }

  /** Why the message given back was not sent, when something stopped it. */
  function takeReturnedNotice(): string | null {
    const notice = returnedNotice.value
    returnedNotice.value = null
    return notice
  }

  /** The stable code behind that reason, when the runtime sent one. */
  function takeReturnedNoticeCode(): string | null {
    const code = returnedNoticeCode.value
    returnedNoticeCode.value = null
    return code
  }

  /** A handed-back message whole, its attachments described by upload name. */
  async function releasedSubmission(input: ReleasedInput): Promise<ComposerSubmission> {
    return {
      text: input.text,
      attachments: await Promise.all(input.attachments.map(submittedAttachment)),
      mentionImages: input.mentionImages,
    }
  }

  /**
   * A queued message as the gateway hands it back. Its attachments come back
   * by id, and each is described by the name it was uploaded under.
   */
  async function queuedSubmission(resp: RunInputResponse): Promise<ComposerSubmission | null> {
    const attachmentIds = Array.isArray(resp.attachments) ? resp.attachments.filter(Boolean) : []
    const submission: ComposerSubmission = {
      text: String(resp.message ?? '').trim(),
      attachments: await Promise.all(attachmentIds.map(submittedAttachment)),
      mentionImages: Array.isArray(resp.mentionImages) ? resp.mentionImages.filter(Boolean) : [],
    }
    if (!submission.text && submission.attachments.length === 0 && submission.mentionImages.length === 0) return null
    return submission
  }

  /**
   * Takes the newest queued message back to edit, whole. A message still held
   * for a run that has not started is the newest there is.
   */
  async function editLastQueuedMessage(): Promise<ComposerSubmission | null> {
    const held = heldBeforeRun.value
    const newest = held[held.length - 1]
    if (newest) {
      heldBeforeRun.value = held.slice(0, -1)
      newest.settle()
      return submissionOf(newest.text, newest.options)
    }
    const runId = activeRunId?.trim()
    if (!runId) return null
    try {
      const resp = await forebrainApi.runQueuedInput(runId, { action: 'edit_last' })
      applyPendingInputPreview(resp.preview)
      if (!resp.accepted) return null
      return queuedSubmission(resp)
    } catch {
      // The run ended between the preview and the key: its queue is being
      // handed back as it ends, so there is nothing left to recall here.
      return null
    }
  }

  /**
   * An attachment known only by id, with the name and type it was uploaded
   * under. The id is what the message carries; if the gateway cannot describe
   * the file, the attachment still comes back and is shown by a generic label.
   */
  async function submittedAttachment(fileId: string): Promise<SubmittedAttachment> {
    try {
      const info = await forebrainApi.fileInfo(fileId)
      return { fileId, filename: String(info.originalName ?? '').trim(), mediaType: String(info.mediaType ?? '').trim() }
    } catch {
      return { fileId, filename: '', mediaType: '' }
    }
  }

  // --- A subagent's own view (plan 005's engine, plan 007's semantics) -------

  /**
   * Queues a message back to a subagent's composer for that view to take. A
   * returned message and whatever that view already holds are one draft, the
   * returned one leading — the same rule the conversation's own returned draft
   * follows.
   */
  function returnSubagentDraft(agentId: string, submission: ComposerSubmission) {
    const id = String(agentId ?? '').trim()
    if (!id) return
    const current = subagentReturnedDraft.value[id]
    subagentReturnedDraft.value = {
      ...subagentReturnedDraft.value,
      [id]: current ? mergeSubmissions(current, submission) : submission,
    }
  }

  /** The composer of that subagent's view takes what was given back to it. */
  function takeSubagentReturnedDraft(agentId: string): ComposerSubmission | null {
    const id = String(agentId ?? '').trim()
    const draft = subagentReturnedDraft.value[id]
    if (!draft) return null
    const next = { ...subagentReturnedDraft.value }
    delete next[id]
    subagentReturnedDraft.value = next
    return draft
  }

  /** A message the engine handed back, whole, its attachments described. */
  async function subagentReturnedSubmission(input: { message?: string; attachments?: string[]; mentionImages?: string[] }): Promise<ComposerSubmission | null> {
    const attachmentIds = Array.isArray(input.attachments) ? input.attachments.filter(Boolean) : []
    const submission: ComposerSubmission = {
      text: String(input.message ?? '').trim(),
      attachments: await Promise.all(attachmentIds.map(submittedAttachment)),
      mentionImages: Array.isArray(input.mentionImages) ? input.mentionImages.filter(Boolean) : [],
    }
    if (!submission.text && submission.attachments.length === 0 && submission.mentionImages.length === 0) return null
    return submission
  }

  /**
   * Sends a message the user typed in a subagent's own view to that subagent.
   * It never touches the primary conversation's send path: the whole exchange
   * belongs to the subagent (rule 9). A refusal never loses the message — it
   * waits in that view's composer.
   */
  async function sendToSubagent(agentId: string, submission: ComposerSubmission, followUp = false): Promise<void> {
    const id = String(agentId ?? '').trim()
    if (!id) return
    const sid = String(sessionId.value ?? '').trim()
    if (!sid) {
      returnSubagentDraft(id, submission)
      return
    }
    const text = String(submission.text ?? '').trim()
    const attachmentIds = (submission.attachments ?? []).map((attachment) => attachment.fileId).filter(Boolean)
    const imagePaths = submission.mentionImages ?? []
    if (!text && attachmentIds.length === 0 && imagePaths.length === 0) return
    try {
      const resp = await forebrainApi.subagentInput(sid, id, {
        message: text,
        attachments: attachmentIds.length ? attachmentIds : undefined,
        mentionImages: imagePaths.length ? imagePaths : undefined,
        mode: followUp ? 'follow_up' : 'steer',
      })
      applyPendingInputPreview(resp.preview, id)
    } catch {
      returnSubagentDraft(id, submission)
    }
  }

  /** Pulls the newest message a subagent has queued back out for editing. */
  async function recallSubagentInput(agentId: string): Promise<ComposerSubmission | null> {
    const id = String(agentId ?? '').trim()
    const sid = String(sessionId.value ?? '').trim()
    if (!id || !sid) return null
    try {
      const resp = await forebrainApi.subagentQueuedInput(sid, id, { action: 'edit_last' })
      applyPendingInputPreview(resp.preview, id)
      if (!resp.accepted) return null
      return queuedSubmission(resp)
    } catch {
      return null
    }
  }

  /** Esc's second meaning in a subagent's view: interrupt it to send now. */
  async function interruptSubagentToSend(agentId: string): Promise<boolean> {
    const id = String(agentId ?? '').trim()
    const sid = String(sessionId.value ?? '').trim()
    if (!id || !sid) return false
    try {
      const resp = await forebrainApi.subagentInterruptSend(sid, id)
      return Boolean(resp.interrupted)
    } catch {
      return false
    }
  }

  /** Esc's first meaning: take the just-sent message back before it answered. */
  async function withdrawSubagentInput(agentId: string): Promise<ComposerSubmission[]> {
    const id = String(agentId ?? '').trim()
    const sid = String(sessionId.value ?? '').trim()
    if (!id || !sid) return []
    try {
      const resp = await forebrainApi.subagentWithdraw(sid, id)
      applyPendingInputPreview(resp.preview, id)
      const out: ComposerSubmission[] = []
      for (const item of resp.withdrawn ?? []) {
        const sub = await subagentReturnedSubmission(item)
        if (sub) out.push(sub)
      }
      return out
    } catch {
      return []
    }
  }

  /** /compact in a subagent's view. 'running' is the 409 the gateway sends. */
  async function compactSubagent(agentId: string): Promise<'ok' | 'running' | 'error'> {
    const id = String(agentId ?? '').trim()
    const sid = String(sessionId.value ?? '').trim()
    if (!id || !sid) return 'error'
    try {
      await forebrainApi.subagentCompact(sid, id)
      return 'ok'
    } catch (err) {
      return responseStatus(err) === 409 ? 'running' : 'error'
    }
  }

  /** /context in a subagent's view, in the conversation's own report shape. */
  async function loadSubagentContext(agentId: string): Promise<SessionContextDebug | null> {
    const id = String(agentId ?? '').trim()
    const sid = String(sessionId.value ?? '').trim()
    if (!id || !sid) return null
    try {
      const data = await forebrainApi.subagentContext(sid, id)
      return data && Object.keys(data as object).length ? data : null
    } catch {
      return null
    }
  }

  /**
   * The gauge a subagent's view opens with: its own context window, measured on
   * the model it runs on. Its token_budget_updated events keep it current.
   */
  async function loadSubagentBudget(agentId: string): Promise<void> {
    const id = String(agentId ?? '').trim()
    const sid = String(sessionId.value ?? '').trim()
    if (!id || !sid) return
    try {
      const budget = await forebrainApi.subagentBudget(sid, id)
      if (budget) updateSubagent(id, (entry) => ({ ...entry, tokenBudget: budget }))
    } catch {
      // The view shows no gauge for a subagent whose budget cannot be read.
    }
  }

  function normalizeMode(v: unknown): SessionMode {
    const raw = String(v ?? '').trim().toLowerCase()
    if (raw === 'plan') return 'plan'
    return 'agent'
  }

  function isPlaceholderPlan(v: ExecutionPlan | null | undefined): boolean {
    if (!v || !Array.isArray(v.steps) || v.steps.length !== 1) return false
    const st = v.steps[0]
    const stepType = String(st.stepType ?? '').trim().toLowerCase()
    const desc = String(st.description ?? '').trim()
    return stepType === 'execute' && desc === 'Process user request and tool actions'
  }

  function shouldRenderPlan(v: ExecutionPlan | null | undefined): boolean {
    if (!v) return false
    if (mode.value === 'plan') return true
    return !isPlaceholderPlan(v)
  }

  function updateMessageById(id: string, updater: (message: ChatMessage) => ChatMessage) {
    messages.value = messages.value.map((message) => {
      if (message.id !== id) return message
      return updater(message)
    })
  }

  /**
   * upsertConversationApproval records a gate on the timeline, beside the call it
   * gated.
   *
   * The record names the call it holds, so when that call is already on screen
   * the line belongs next to it - directly above the card it answers, which is
   * where the terminal puts it. Only a gate whose call this client cannot place
   * (an event from a run with no transcript row of its own, or one that names no
   * call at all) falls back to the run's own projection.
   */
  function upsertConversationApproval(
    assistantMessageId: string,
    toolStepId: string,
    block: Extract<TimelineBlock, { kind: 'approval' }>,
  ) {
    const owner = toolStepId
      ? messages.value.find((message) => (message.blocks ?? []).some(
        (existing) => existing.kind === 'tool' && existing.step.stepId === toolStepId,
      ) || message.gateStepIds?.includes(toolStepId))
      : undefined
    if (!owner?.id) {
      updateMessageById(assistantMessageId, (message) => ({
        ...message,
        blocks: upsertApprovalBlock(message.blocks ?? [], block),
      }))
      return
    }
    updateMessageById(owner.id, (message) => ({
      ...message,
      blocks: upsertApprovalBlock(message.blocks ?? [], block),
    }))
  }

  function handleStepEvent(
    evt: {
      kind?: string
      stepId?: string
      description?: string
      data?: string | Record<string, unknown> | null
      durationSeconds?: number
      currentQuery?: string | null
      id?: string
    },
    assistantMessageId: string,
  ) {
    if (evt.kind !== 'tool_call_started' && evt.kind !== 'tool_call_completed') {
      return
    }
    const completed = evt.kind === 'tool_call_completed'
    const step = toolStepFromPayload(
      evt as unknown as Record<string, unknown>,
      completed,
      `${String(evt.description ?? 'tool')}-${evt.id ?? ''}`,
    )
    // The exit-plan gate's wait paints no card: its approval prompt is the
    // whole wait (stepHoldsNoCard).
    if (stepHoldsNoCard(step)) return
    rememberSubagentCallStep(step)
    updateMessageById(assistantMessageId, (message) => ({
      ...message,
      // A call ends the response that preceded it - the runtime writes that
      // response as its own row - so the text before it stays its own block
      // instead of running into whatever is said afterwards.
      blocks: upsertToolBlock(
        closeStreamedBlocks(message.blocks ?? [], ['assistant', 'thinking']),
        step,
        evt.id,
      ),
    }))
  }

  function handleRunEvent(
    evt: ForebrainRunEvent,
    assistantMessageId: string,
    planBlocksAccumulator: PlanBlock[],
    state: RunProjectionState,
  ) {
	const eventID = String(evt.id ?? '').trim()
	if (eventID && appliedEventIDs.has(eventID)) return
	if (eventID) appliedEventIDs.add(eventID)
	if (Number.isFinite(evt.sequence) && Number(evt.sequence) > sessionEventCursor) {
	  sessionEventCursor = Number(evt.sequence)
	}
    const payload = evt.payload ?? {}
    // A run's output lands in its latest answer: the one opened after the
    // newest steer the run handed its model (see input_delivered), or the
    // answer the run started with.
    assistantMessageId = answerTarget(state, assistantMessageId)
    if (evt.type === 'approval_requested' || evt.type === 'approval_resolved') {
      // The global pending-action controls and the per-agent history card read
      // one durable action. Refresh on both edges so a decision made in another
      // tab removes stale buttons immediately in this one.
      pendingActionsVersion.value += 1
    }
    // A plan review changes what the parked exit-plan approval's card shows:
    // while it runs the card says so, and once it closes the card offers the
    // review with its choices again. Both facts live in these events.
    if (evt.type === 'plan_review_started' || evt.type === 'plan_reviewed') {
      pendingActionsVersion.value += 1
    }
    // A subagent's spawn opens its transcript with the prompt it was given, and
    // its end closes it. Everything in between that carries its roster key is
    // routed there too, and never into the conversation.
    if (evt.type === 'subagent_spawned') {
      const agentId = String(payload.agentId ?? '').trim()
      const agentType = String(payload.agentType ?? '').trim()
      if (agentType === FOREBRAIN_GOAL_CHECK_AGENT_TYPE) setGoalChecking(true)
      const task = String(payload.task ?? '').trim()
      const title = String(payload.title ?? '').trim()
	  const executionId = String(payload.executionId ?? evt.runId ?? evt.id ?? '').trim()
	  const promptId = executionId ? `prompt:${executionId}` : undefined
      // A user-driven execution's prompt is the user's own message, drawn as
      // such rather than as the dispatching agent's instruction.
      const fromUser = String(payload.origin ?? '').trim() === 'user'
      updateSubagent(agentId, (entry) => ({
        ...entry,
        agentType: agentType || entry.agentType,
        title: title || entry.title,
        task: entry.task || task,
        status: 'running',
		error: undefined,
        startedAt: evt.createdAt ?? entry.startedAt,
        // The model is the engine's own resolution, read — not derived here.
        modelProvider: String(payload.modelProvider ?? '').trim() || undefined,
        model: String(payload.model ?? '').trim() || undefined,
        reasoningEffort: String(payload.reasoningEffort ?? '').trim() || undefined,
        executionStarts: executionId
          ? { ...(entry.executionStarts ?? {}), [executionId]: evt.createdAt ?? entry.startedAt ?? '' }
          : entry.executionStarts,
        // Every execution has its own prompt. The first dispatch opens the
        // transcript; continue appends after closing the previous stream.
        blocks: !task || entry.blocks.some((b) => (b.kind === 'prompt' || b.kind === 'user') && b.id === promptId)
          ? entry.blocks
          : [...closeStreamedBlocks(entry.blocks, ['assistant', 'thinking'], evt.createdAt), fromUser
              ? { kind: 'user' as const, id: promptId, text: task }
              : { kind: 'prompt' as const, id: promptId, text: task }],
      }))
      // The conversation's account of the subagent is its task row on the
      // dispatching call's card — bound here, settled by its end.
      bindSubagentSpawn(evt, payload as Record<string, unknown>, assistantMessageId)
      return
    }
    if (evt.type === 'subagent_ended') {
      const agentId = String(payload.agentId ?? '').trim()
      const agentType = String(payload.agentType ?? '').trim()
      if (agentType === FOREBRAIN_GOAL_CHECK_AGENT_TYPE) setGoalChecking(false)
      const status = String(payload.status ?? '').trim()
      const errText = String(payload.error ?? '').trim()
	  const normalizedStatus = status.toLowerCase()
	  const transcriptStatus: SubagentTranscript['status'] = normalizedStatus === 'cancelled'
	    ? 'cancelled'
	    : normalizedStatus === 'interrupted'
	      ? 'interrupted'
	      : Boolean(errText) || normalizedStatus === 'failed'
	        ? 'failed'
	        : 'done'
	  const output = String(payload.output ?? '').trim()
	  const executionId = String(payload.executionId ?? evt.runId ?? evt.id ?? '').trim()
	  const promptId = executionId ? `prompt:${executionId}` : undefined
	  // This execution's end: the reaper stamps it, and an ordinary end is the
	  // event's own arrival.
	  const finishedAtMsRaw = Number(payload.finishedAtMs)
	  const finishedAtMs = Number.isFinite(finishedAtMsRaw) && finishedAtMsRaw > 0
		? finishedAtMsRaw
		: eventTimestamp(evt.createdAt)
      updateSubagent(agentId, (entry) => ({
        ...entry,
        agentType: agentType || entry.agentType,
	    status: transcriptStatus,
		error: errText || undefined,
        finishedAt: evt.createdAt ?? entry.finishedAt,
        // The run is over, so nothing more will be appended to the text it was
        // streaming.
	    blocks: (() => {
		  let blocks = closeStreamedBlocks(entry.blocks, ['assistant', 'thinking'], evt.createdAt)
		  let promptIndex = -1
		  if (promptId) {
			for (let i = blocks.length - 1; i >= 0; i--) {
			  const block = blocks[i]
			  if (block?.kind === 'prompt' && block.id === promptId) {
				promptIndex = i
				break
			  }
			}
		  }
		  const currentBlocks = promptIndex >= 0 ? blocks.slice(promptIndex + 1) : blocks
		  if (output && !currentBlocks.some((block) => block.kind === 'assistant')) {
			blocks = [...blocks, { kind: 'assistant' as const, id: executionId ? `final:${executionId}` : undefined, text: output, open: false }]
		  }
		  if (errText && !blocks.some((block) => block.kind === 'error' && block.id === `error:${executionId}`)) {
			blocks = [...blocks, { kind: 'error' as const, id: executionId ? `error:${executionId}` : undefined, text: errText }]
		  }
		  // Every execution closes with its own worked line — how long this
		  // one ran, from its own spawn to this end, and the checklist state
		  // it ended with. A redelivered end is one delivery too many, not a
		  // second execution.
		  const startRaw = executionId ? entry.executionStarts?.[executionId] : undefined
		  const startedAtMs = startRaw ? Date.parse(startRaw) : NaN
		  if (executionId && Number.isFinite(startedAtMs) &&
		      !blocks.some((block) => block.kind === 'worked' && block.id === `worked:${executionId}`)) {
			blocks = [...blocks, {
			  kind: 'worked' as const,
			  id: `worked:${executionId}`,
			  durationMs: Math.max(0, finishedAtMs - startedAtMs),
			  finishedAt: new Date(finishedAtMs).toISOString(),
			  ...(entry.plan && entry.plan.total > 0 ? { plan: entry.plan } : {}),
			}]
		  }
		  return blocks
	    })(),
      }))
      // The card task this execution was bound to takes its outcome and its
      // stop time; the query rows that reported it end with it.
      settleSubagentEnd(evt, payload as Record<string, unknown>, assistantMessageId)
      return
    }
    const subagentID = subagentIdFromPayload(payload as Record<string, unknown>)
    if (subagentID && routeSubagentEvent(subagentID, evt, payload as Record<string, unknown>)) {
      return
    }
    // A plan review requested from the exit-plan approval is itself the
    // dispatch: one card, one reviewer task, no subject in its header (D13).
    if (evt.type === 'plan_review_started') {
      const reviewId = String(payload.reviewId ?? '').trim()
      if (!reviewId) return
      const provider = String(payload.provider ?? '').trim()
      const model = String(payload.model ?? '').trim()
      const label = String(payload.label ?? '').trim() || [provider, model].filter(Boolean).join('/')
      upsertSubagentExecutionBlock(evt, `plan-review:${reviewId}`, {
        stepId: reviewId,
        toolName: '',
        summary: label ? `review the plan with ${label}` : 'review the plan',
        status: 'running',
        subagentCall: {
          verb: 'review',
          tasks: [{ index: 0, title: 'Plan review', agentType: 'plan-reviewer', status: 'waiting' }],
        },
      }, assistantMessageId)
      // The reviewer's spawned event names the review as its parent tool
      // call, which is how it finds this card.
      subagentCallStepIds.add(reviewId)
      return
    }
    if (evt.type === 'plan_reviewed') {
      const reviewId = String(payload.reviewId ?? '').trim()
      const outcome = String(payload.outcome ?? '').trim().toLowerCase()
      const errText = String(payload.error ?? '').trim()
      const finishedAtMs = eventTimestamp(evt.createdAt)
      const durationMs = Number(payload.durationMs)
      // The review's call-level outcome, written back onto the card's own
      // step so its header phase follows it.
      const patchReviewStep = (patch: Partial<SubagentToolStep>) => {
        for (const message of messages.value) {
          const holds = (message.blocks ?? []).some((b) => b.kind === 'subagent' && b.stepId === reviewId)
          if (!holds || !message.id) continue
          updateMessageById(message.id, (m) => ({
            ...m,
            blocks: (m.blocks ?? []).map((b) => (
              b.kind === 'subagent' && b.stepId === reviewId
                ? { ...b, step: { ...b.step, ...patch } }
                : b
            )),
          }))
          return
        }
      }
      const tasks = subagentCallTasks.get(reviewId)
      const task = tasks?.get(0)
      const taskTerminal = Boolean(task && subagentTaskTerminal(String(task.status ?? '')))
      if (outcome === 'stopped') {
        // A review stopped before its reviewer started was canceled as a
        // call; once one ran, its own ended event settles the task.
        if (!taskTerminal) {
          const next = new Map(tasks ?? [])
          next.set(0, { ...(task ?? { agentId: '' }), status: 'cancelled', finishedAtMs: task?.finishedAtMs ?? finishedAtMs })
          subagentCallTasks.set(reviewId, next)
          patchReviewStep({ status: 'canceled' })
          syncSubagentClock()
        }
        return
      }
      if (outcome === 'failed' || outcome === 'timed_out') {
        if (!taskTerminal) {
          // The reviewer never started: the request itself failed.
          const next = new Map(tasks ?? [])
          next.set(0, {
            ...(task ?? { agentId: '' }),
            agentId: task?.agentId ?? '',
            status: 'failed',
            error: errText || task?.error,
            startedAtMs: task?.startedAtMs,
            finishedAtMs: task?.finishedAtMs ?? (
              Number.isFinite(durationMs) && durationMs > 0 ? finishedAtMs - durationMs : finishedAtMs
            ),
          })
          subagentCallTasks.set(reviewId, next)
          patchReviewStep({ status: 'failed', error: errText || undefined })
          syncSubagentClock()
          return
        }
        // The reviewer ran and ended; this only adds why no review came.
        if (errText && tasks && task) {
          tasks.set(0, { ...task, error: errText })
          subagentCallTasks.set(reviewId, tasks)
        }
        return
      }
      // done: the reviewer's own ended event settles the task; this closes
      // the card's call.
      patchReviewStep({ status: 'completed' })
      return
    }
    switch (evt.type) {
      case 'turn_started':
        setError(null)
        activeRunId = String(evt.runId ?? '').trim() || null
        if (activeRunId) void flushHeldIntoRun(activeRunId)
		if (activeRunId) {
		  eventProjectionByRun.set(activeRunId, { assistantMessageId, planBlocks: planBlocksAccumulator, state })
		}
        contextSignals.value = { ...contextSignals.value, activeRunId: activeRunId ?? undefined }
        state.runStartedAt = evt.createdAt
        updateMessageById(assistantMessageId, (message) => ({
          ...message,
		  runId: activeRunId ?? message.runId,
          ...(evt.createdAt ? { runStartedAt: evt.createdAt } : {}),
        }))
        return
      case 'mode_changed': {
        const data = payload.mode && typeof payload.mode === 'object' ? payload.mode as Record<string, unknown> : payload
        mode.value = normalizeMode(data.mode)
        modePhase.value = String(data.phase ?? '').trim()
        return
      }
      case 'tool_call_started':
        handleStepEvent({
          ...(payload as Record<string, unknown>),
          kind: String(payload.kind ?? 'tool_call_started'),
          id: evt.id,
        }, assistantMessageId)
        if (String(payload.toolName ?? '').trim().toLowerCase() === 'shell') {
          updateRuntimeKind('working')
          runtimeStatus.value = { ...runtimeStatus.value, toolName: 'shell' }
        }
        return
      case 'tool_call_completed':
        handleStepEvent({
          ...(payload as Record<string, unknown>),
          kind: String(payload.kind ?? 'tool_call_completed'),
          id: evt.id,
        }, assistantMessageId)
        if (String(payload.toolName ?? '').trim().toLowerCase() === 'shell') {
          updateRuntimeKind('working')
          runtimeStatus.value = { ...runtimeStatus.value, toolName: undefined }
        }
        return
      case 'turn_diff_updated': {
        const turnDiffs = parseTurnDiffPayload(payload)
        if (turnDiffs.length === 0) return
        updateMessageById(assistantMessageId, (message) => ({
          ...message,
          turnDiffs: [...(message.turnDiffs ?? []), ...turnDiffs],
        }))
        return
      }
      case 'input_delivered': {
        // A steer the run handed its model is the user's own message, drawn
        // where the model received it: the answer so far closes, the message
        // follows it, and what the run says next opens a new answer after it —
        // the shape a reload draws from the transcript, and the place the
        // terminal draws it live.
        const delivered: ReleasedInput = {
          text: String(payload.text ?? ''),
          attachments: stringList(payload.attachments),
          mentionImages: stringList(payload.mentionImages),
        }
        if (!delivered.text.trim() && delivered.attachments.length === 0 && delivered.mentionImages.length === 0) return
        const seed = eventID || `${Date.now()}`
        const userId = `user-delivered-${seed}`
        const nextAnswerId = `assistant-delivered-${seed}`
        const runId = String(evt.runId ?? '').trim() || undefined
        const inserted: ChatMessage[] = [
          { id: userId, role: 'user', content: delivered.text, runId },
          { id: nextAnswerId, role: 'assistant', content: NOTHING_SAID_YET, runId },
        ]
        const index = messages.value.findIndex((message) => message.id === assistantMessageId)
        if (index >= 0) {
          const closing = messages.value[index]!
          const blocks = closeStreamedBlocks(closing.blocks ?? [], ['assistant', 'thinking'], evt.createdAt)
          messages.value = [
            ...messages.value.slice(0, index),
            { ...closing, blocks, content: spokenText(blocks) },
            ...inserted,
            ...messages.value.slice(index + 1),
          ]
        } else {
          messages.value = [...messages.value, ...inserted]
        }
        state.answerMessageId = nextAnswerId
        // The answer text the run accumulates is the new answer's alone now.
        state.fullAnswer = ''
        answer.value = ''
        if (delivered.attachments.length || delivered.mentionImages.length) {
          // What it attached is described by name once the gateway answers.
          void releasedSubmission(delivered).then((submission) => {
            const attachments = messageAttachments(submission)
            updateMessageById(userId, (message) => ({
              ...message,
              content: message.content.trim() ? message.content : attachedOnlyText(attachments),
              attachments,
            }))
          })
        }
        return
      }
      case 'pending_input_updated':
        applyPendingInputPreview(payload)
        return
      case 'goal_started':
      case 'goal_round_started':
      case 'goal_completed': {
        const goal = parseGoalEvent(evt.type, payload as Record<string, unknown>)
        if (!goal) return
        updateMessageById(assistantMessageId, (message) => ({
          ...message,
          blocks: appendGoalLine(message.blocks ?? [], goal, evt.createdAt),
        }))
        if (runtimeStatus.value.kind !== 'idle') {
          runtimeStatus.value = {
            ...runtimeStatus.value,
            goalRound: goal.phase === 'completed' ? undefined : goal.round ?? 1,
            goalChecking: false,
          }
        }
        return
      }
      case 'queued_input_released': {
        // The run has ended and handed back every message it never took; the
        // queue it previewed went with it. The engine already decided what
        // follows the run: the messages in next run as the following turn,
        // the ones in inputs return to the composer — this page only obeys.
        serverPendingInput.value = emptyPendingInputPreview()
        const giveBackReleased = (inputs: ReleasedInput[]) => {
          if (!inputs.length) return
          void Promise.all(inputs.map(releasedSubmission)).then((submissions) => submissions.forEach((submission) => giveBack(submission)))
        }
        if (state.awaitingRelease) {
          state.released = [...(state.released ?? []), ...releasedNextInputs(payload)]
          giveBackReleased(releasedInputs(payload))
        } else if (state.sentHere) {
          // The send that started the run has already ended, so there is no
          // next turn of its own to put anything in: all of it goes back to
          // the composer.
          giveBackReleased(releasedInputs(payload))
          giveBackReleased(releasedNextInputs(payload))
        }
        return
      }
      case 'context_compacting':
      case 'context_compact_progress':
      case 'context_compacted':
      case 'context_compact_failed': {
        const update = parseCompactionEvent(evt.type, payload as Record<string, unknown>)
        if (!update) return
        updateMessageById(compactionHostId(update.compactionId) ?? assistantMessageId, (message) => ({
          ...message,
          blocks: applyCompactionUpdate(message.blocks ?? [], update, evt.createdAt),
        }))
        if (evt.type === 'context_compacted') {
          contextSignals.value = {
            ...contextSignals.value,
            compactVersion: contextSignals.value.compactVersion + 1,
          }
        }
        return
      }
      case 'token_budget_updated':
        updateMessageById(assistantMessageId, (message) => applyAssistantRunEventMetadata(message, evt))
        contextSignals.value = {
          ...contextSignals.value,
          budgetVersion: contextSignals.value.budgetVersion + 1,
        }
        return
      case 'plan_updated':
        {
          const planUpdate = parsePlanUpdatePayload(payload)
          if (planUpdate) {
            applyPlanRuntimeStatus(planUpdate)
            updateMessageById(assistantMessageId, (message) => applyPlanUpdateToMessage(message, planUpdate))
          } else {
            updateMessageById(assistantMessageId, (message) => applyAssistantRunEventMetadata(message, evt))
          }
        }
        return
      case 'assistant_delta': {
        const txt = String(payload.text ?? '')
        state.fullAnswer += txt
        answer.value = state.fullAnswer
        updateMessageById(assistantMessageId, (message) => withSpokenDelta(message, txt, evt.id, evt.createdAt))
        return
      }
      case 'reasoning_delta': {
        const txt = String(payload.text ?? '')
        if (!txt) return
        updateMessageById(assistantMessageId, (message) => ({
          ...message,
          // Thinking after text closes the text block, the way text closes
          // the thinking block: a block of one kind ends when a block of the
          // other starts, in either direction.
          blocks: appendStreamedText(
            closeStreamedBlocks(message.blocks ?? [], ['assistant'], evt.createdAt),
            'thinking',
            txt,
            evt.id,
            evt.createdAt,
          ),
        }))
        return
      }
      case 'reasoning_done':
        updateMessageById(assistantMessageId, (message) => ({
          ...message,
          blocks: closeStreamedBlocks(message.blocks ?? [], ['thinking'], evt.createdAt),
        }))
        return
      case 'turn_completed': {
        state.completedNormally = true
        state.completedRunId = String(evt.runId ?? activeRunId ?? '').trim() || state.completedRunId || null
        const finalText = String(payload.text ?? '')
        if (finalText && finalText.length >= state.fullAnswer.length) {
          state.fullAnswer = finalText
        }
        if (payload.pendingInput) {
          applyPendingInputPreview(payload.pendingInput)
        }
        updateMessageById(assistantMessageId, (message) => ({
          ...withSettledTurnText(message, finalText, evt.createdAt),
          ...runEndPatch(evt.createdAt, payload.elapsedMs, state.runStartedAt, planProgressOfPayload(payload)),
        }))
        runtimeStatus.value = { ...runtimeStatus.value, toolName: undefined }
        isStreaming.value = false
        return
      }
      case 'turn_error': {
        state.completedNormally = false
        const msg = String(payload.error ?? payload.message ?? 'Request failed')
        updateMessageById(assistantMessageId, (message) => ({
          ...withRunError(message, evt.runId, msg, parseProviderErrorDetail(payload.detail), evt.createdAt),
          ...runEndPatch(evt.createdAt, payload.elapsedMs, state.runStartedAt, planProgressOfPayload(payload)),
        }))
        runtimeStatus.value = { ...runtimeStatus.value, toolName: undefined }
        isStreaming.value = false
        return
      }
      case 'approval_requested': {
        // A gate on one of the conversation's own calls. The card is the call,
        // so this registers that the call is waiting - and it is the record that
        // survives a reload, which is what makes a canceled gate visible here at
        // all.
        const actionId = String(payload.actionId ?? '').trim()
        if (!actionId) return
        upsertConversationApproval(assistantMessageId, String(payload.toolStepId ?? '').trim(), {
          kind: 'approval',
          id: `approval-${actionId}`,
          actionId,
          actionKind: String(payload.actionKind ?? '').trim(),
          status: 'pending',
          message: String(payload.message ?? '').trim() || undefined,
          toolStepId: String(payload.toolStepId ?? '').trim() || undefined,
        })
        return
      }
      case 'approval_resolved': {
        // The decision, with the one line the surface printed for it. Rendering
        // that line rather than rebuilding it keeps the web and the terminal
        // word-for-word identical, including wording only the surface knows.
        // A delivered review's record carries the internal handoff key instead
        // of user-facing words, so its line is dropped and the card closes
        // silently - the delivery was never something to read.
        const actionId = String(payload.actionId ?? '').trim()
        if (!actionId) return
        const confirmationRaw = String(payload.confirmation ?? '').trim()
        upsertConversationApproval(assistantMessageId, String(payload.toolStepId ?? '').trim(), {
          kind: 'approval',
          id: `approval-${actionId}`,
          actionId,
          actionKind: String(payload.actionKind ?? '').trim(),
          status: normalizeApprovalDecision(String(payload.decision ?? payload.status ?? 'resolved').trim()),
          confirmation: confirmationRaw && confirmationRaw !== PLAN_REVIEW_DELIVERED_KEY ? confirmationRaw : undefined,
          message: String(payload.reason ?? '').trim() || undefined,
          toolStepId: String(payload.toolStepId ?? '').trim() || undefined,
        })
        return
      }
      case 'turn_cancelled': {
        state.completedNormally = false
        state.completedRunId = String(evt.runId ?? activeRunId ?? '').trim() || state.completedRunId || null
        updateMessageById(assistantMessageId, (message) => ({
          ...withCancelledTurn(message, evt.createdAt),
          ...runEndPatch(evt.createdAt, payload.elapsedMs, state.runStartedAt, planProgressOfPayload(payload)),
        }))
        runtimeStatus.value = { ...runtimeStatus.value, toolName: undefined }
        isStreaming.value = false
        return
      }
    }
  }

  /**
   * Sends a message. A message that never becomes a turn — withdrawn before
   * it began, or not taken by a run that had just ended — goes back to the
   * composer through the returned draft, never lost.
   */
  /**
   * Answers the picker on a notice: the notice keeps what was picked, and the
   * pick goes back to the command that offered it.
   */
  async function choose(noticeId: string, choice: SlashChoice): Promise<void> {
    updateMessageById(noticeId, (message) => ({ ...message, picked: choice.value }))
    if (choice.command === 'skills') {
      // A skill picked from the list runs as its own command does, and the
      // conversation shows it was asked for.
      await send(`/${choice.value}`)
      return
    }
    await send(`/${choice.command}`, { choice })
  }

  /**
   * cancelAutoContinue stops the continuation the view is waiting to run — the
   * conversation's own with no agentId, a subagent's with its roster key. The
   * notice goes at once; the runtime's own cancellation event follows on every
   * page watching the session.
   */
  function cancelAutoContinue(agentId = ''): boolean {
    const key = String(agentId ?? '').trim()
    if (key) {
      if (!subagentAutoContinue.value[key]) return false
      setAutoContinueFor(key, null)
      const sid = String(sessionId.value ?? '').trim()
      if (sid) void forebrainApi.cancelAutoContinue(sid, key).catch(() => { /* the wait may already have ended */ })
      return true
    }
    if (!autoContinue.value) return false
    autoContinue.value = null
    const sid = String(sessionId.value ?? '').trim()
    if (sid) void forebrainApi.cancelAutoContinue(sid).catch(() => { /* the wait may already have ended */ })
    return true
  }

  /** autoContinueForView is the wait to show in one view: "" the conversation's. */
  function autoContinueForView(view: string): AutoContinueState | null {
    const key = String(view ?? '').trim()
    if (!key) return autoContinue.value
    return subagentAutoContinue.value[key] ?? null
  }

  // A history snapshot that began before a local send into the same session
  // must not apply after it, or its pre-send (empty) view erases the turn.
  // Each session remembers the epoch of its latest send; a snapshot taken at
  // epoch N is stale for that session only when a send came after it (> N) —
  // a send before the snapshot is already in what the snapshot reads.
  const lastSendEpochBySession = new Map<string, number>()
  let sendEpoch = 0

  async function send(userMessage: string, options?: SendOptions): Promise<void> {
    setError(null)
    // Sending moves the conversation on without the continuation. The runtime
    // supersedes it when the turn starts anyway; cancelling here also covers a
    // send that never becomes a turn, such as a slash command.
    cancelAutoContinue()
    sendEpoch++
    const sendSession = String(options?.sessionId ?? sessionId.value ?? '').trim()
    if (sendSession) lastSendEpochBySession.set(sendSession, sendEpoch)
    if (isStreaming.value && activeRunId) {
      const disposition = options?.activeInputDisposition === 'queue' ? 'queue' : 'steer'
      if (!await queueActiveRunInput(activeRunId, userMessage, disposition, options?.attached)) {
        giveBack(submissionOf(userMessage, options))
      }
      return
    }
    if (isStreaming.value) {
      // Another send is in flight and its run does not exist yet: this one
      // follows it rather than starting a second, concurrent send.
      return new Promise<void>((settle) => {
        heldBeforeRun.value = [...heldBeforeRun.value, { text: userMessage, options: options ?? {}, settle }]
      })
    }
    let withdrawn = false
    let withdrawnError = ''
    let withdrawnCode = ''
    // The conversation this send belongs to: the one it entered with, or the
    // one its socket bound when it started a new conversation. What waits on
    // this send follows that conversation, never the one on screen when it
    // ends.
    let runSession = sendSession
    plan.value = null
    answer.value = ''
    serverPendingInput.value = emptyPendingInputPreview()
    const runtimeStartedAt = Date.now()
    startRuntimeStatus('working', runtimeStartedAt)
    const planBlocksAccumulator: PlanBlock[] = []
    isStreaming.value = true
    const messageSeed = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    const userMessageId = `user-${messageSeed}`
    const assistantMessageId = `assistant-${messageSeed}`
    const userAttachments = messageAttachments(options?.attached)
    messages.value = [
      ...messages.value,
      ...(options?.choice ? [] : [{
        id: userMessageId,
        role: 'user' as const,
        content: userMessage.trim() ? userMessage : attachedOnlyText(userAttachments),
        attachments: userAttachments.length ? userAttachments : undefined,
      }]),
      { id: assistantMessageId, role: 'assistant', content: NOTHING_SAID_YET },
    ]

    // This send's view of its run. It hears the messages its run hands back
    // until it acts on them at its end; later, they go back to the composer.
    const eventState: RunProjectionState = { fullAnswer: '', sentHere: true, awaitingRelease: true, released: [] }
    // The answer this send's run is drawing into now (see answerTarget).
    const answerId = () => answerTarget(eventState, assistantMessageId)
    // The controller this send owns. A later send, or a session reset,
    // replaces the global one before this send ends; the cleanup at the end
    // answers to whoever owns the slot then, not to every send that passed
    // through it.
    const myController = streamAbortController = new AbortController()
    try {
      const currentSessionId = options?.sessionId ?? sessionId.value ?? undefined
      const attachmentIds = (options?.attached?.attachments ?? []).map((attachment) => attachment.fileId)
      const mentionImagePaths = options?.attached?.mentionImages ?? []
      const wsUrl = buildBrowserForebrainGatewayChatWsUrl()
      const requestId = `req-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
      const handleOp = (op: string, payload: Record<string, unknown>) => {
        if (!op) return
        if (op === 'session_bound') {
          const sid = String(payload.sessionId ?? '').trim()
          if (sid) runSession = sid
          const sessionSwitched = payload.sessionSwitched === true
          if (sessionSwitched) {
            applySessionReset({ sid, loadMessages: true, loadMode: true })
          } else {
            sessionId.value = sid || null
            if (sid) persistLastSessionId(sid)
			if (sid) void startSessionObserver(sid)
          }
          return { terminal: sessionSwitched }
        }
        if (op === 'slash_reply') {
          // Slash replies are UI-only output: never persisted in the
          // transcript, never sent to the model, and no run lifecycle rides
          // along. The placeholder turn bubble becomes a system notice; a
          // page refresh drops it, which is the point.
          const text = String(payload.text ?? '')
          const data = payload.data && typeof payload.data === 'object' ? payload.data as Record<string, unknown> : {}
          const picker = slashPickerOf(data.picker)
          if (text.trim() || picker) {
            // fullAnswer marks the turn as having data, so the send-path
            // cleanup keeps the message instead of dropping the placeholder.
            eventState.fullAnswer = text
            updateMessageById(assistantMessageId, () => ({
              id: assistantMessageId,
              role: 'notice' as const,
              content: text,
              ...(picker ? { picker } : {}),
            }))
          }
          // An empty reply means the command drew its own result — /compact's
          // card, built from its events on this same turn — and there is no
          // notice to add: the turn keeps what it drew, and one that drew
          // nothing is dropped by the send-path cleanup.
          eventState.completedNormally = true
          if (streamAbortController === myController) {
            isStreaming.value = false
          }
          return { terminal: true }
        }
        if (op === 'run_started') {
          const rid = String(payload.runId ?? '').trim()
          activeRunId = rid || null
          if (activeRunId) void flushHeldIntoRun(activeRunId)
		  if (activeRunId) {
			legacyMirroredRunIDs.add(activeRunId)
			eventProjectionByRun.set(activeRunId, { assistantMessageId, planBlocks: planBlocksAccumulator, state: eventState })
		  }
          contextSignals.value = { ...contextSignals.value, activeRunId: activeRunId ?? undefined }
          // The op carries no time of its own; the run began when it was
          // heard, on the same clock its end is heard on.
          eventState.runStartedAt = String(payload.createdAt ?? payload.startedAt ?? '').trim() || new Date().toISOString()
          updateMessageById(assistantMessageId, (message) => ({
            ...message,
			runId: rid || message.runId,
            ...(eventState.runStartedAt ? { runStartedAt: eventState.runStartedAt } : {}),
          }))
          return
        }
        if (op === 'mode_changed') {
          const data = (payload.data && typeof payload.data === 'object') ? payload.data as Record<string, unknown> : payload
          mode.value = normalizeMode(data.mode)
          modePhase.value = String(data.phase ?? '').trim()
          return
        }
        if (op === 'run_completed') {
          eventState.completedNormally = true
          eventState.completedRunId = String(payload.runId ?? activeRunId ?? '').trim() || eventState.completedRunId || null
          const finalText = String(payload.text ?? '')
          if (finalText && finalText.length >= eventState.fullAnswer.length) {
            eventState.fullAnswer = finalText
          }
          if (payload.pendingInput) {
            applyPendingInputPreview(payload.pendingInput)
          }
          const finishedAt = String(payload.createdAt ?? payload.finishedAt ?? new Date().toISOString()).trim()
          const data = (payload.data && typeof payload.data === 'object')
            ? toCamelCase(payload.data as Record<string, unknown>)
            : payload
          updateMessageById(answerId(), (message) => ({
            ...withSettledTurnText(message, finalText, finishedAt),
            ...runEndPatch(finishedAt, data.elapsedMs, eventState.runStartedAt, planProgressOfPayload(payload)),
          }))
          if (streamAbortController === myController) {
            isStreaming.value = false
          }
          return
        }
        if (op === 'pending_input_updated') {
          applyPendingInputPreview(payload.data ?? payload)
          return
        }
        if (op === 'run_error') {
          eventState.completedNormally = false
          const msg = String(payload.error ?? payload.message ?? 'Request failed')
          const finishedAt = String(payload.createdAt ?? payload.finishedAt ?? new Date().toISOString()).trim()
          updateMessageById(answerId(), (message) => ({
            ...withRunError(message, String(payload.runId ?? activeRunId ?? '').trim(), msg, parseProviderErrorDetail(payload.data), finishedAt),
            ...runEndPatch(finishedAt, undefined, eventState.runStartedAt, planProgressOfPayload(payload)),
          }))
          if (streamAbortController === myController) {
            isStreaming.value = false
          }
          return
        }
        if (op === 'requires_action') {
          pendingActionsVersion.value += 1
          // The run is parked on a decision; after it, the gateway drives
          // the run without this socket's legacy operations, so its events
          // are the only account of it and must not be set aside as mirrors.
          // The gateway sent every mirror it owed before this.
          if (activeRunId) legacyMirroredRunIDs.delete(activeRunId)
          return
        }
        if (op === 'turn_withdrawn') {
          // The message never became a turn: the user stopped the compaction
          // it was waiting on (whose card stays to say it was cancelled), or
          // something it attached could not be made available, which the
          // error says. It leaves the conversation and goes back to the
          // composer whole.
          withdrawn = true
          withdrawnError = String(payload.error ?? '').trim()
          // A refusal the runtime could classify carries its code alongside
          // the sentence it rendered, so the notice can say it in the
          // viewer's language instead; an unclassified one keeps only the
          // sentence.
          withdrawnCode = parseProviderErrorDetail(payload.data)?.code ?? ''
          messages.value = messages.value.filter((message) => message.id !== userMessageId)
          if (streamAbortController === myController) {
            isStreaming.value = false
          }
          return { terminal: true }
        }
        if (op === 'run_cancelled') {
          eventState.completedNormally = false
          eventState.completedRunId = String(payload.runId ?? activeRunId ?? '').trim() || eventState.completedRunId || null
          const finishedAt = String(payload.createdAt ?? payload.finishedAt ?? new Date().toISOString()).trim()
          updateMessageById(answerId(), (message) => ({
            ...withCancelledTurn(message, finishedAt),
            ...runEndPatch(finishedAt, undefined, eventState.runStartedAt, planProgressOfPayload(payload)),
          }))
          if (streamAbortController === myController) {
            isStreaming.value = false
          }
          return
        }
      }
      const connectOnce = (resume: boolean, attempt = 0) => new Promise<void>((resolve, reject) => {
        const ws = new WebSocket(wsUrl)
        activeSocket = ws
        let terminal = false
        let opened = false
        const clearSocket = () => {
          if (activeSocket === ws) {
            activeSocket = null
          }
        }
        const onAbort = () => {
          // A send that has not started its run yet has nothing to cancel by
          // id; closing the socket is how it stops.
          if (activeRunId) {
            try {
              ws.send(JSON.stringify({
                op: 'cancel_run',
                protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
                request_id: requestId,
                run_id: activeRunId,
              }))
            } catch {
              //
            }
          }
          try {
            ws.close()
          } catch {
            //
          }
        }
        streamAbortController?.signal.addEventListener('abort', onAbort, { once: true })
        ws.onopen = () => {
          opened = true
          if (resume && activeRunId) {
            // Keep the already projected prefix. The durable replay starts
            // after this cursor; clearing text/tool state here would make
            // deduplication discard the only copy of the partial response.
            ws.send(JSON.stringify({
              op: 'resume_connection',
			  protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
              request_id: `${requestId}-resume-${attempt}`,
              run_id: activeRunId,
              session_id: sessionId.value ?? currentSessionId,
			  cursor: sessionEventCursor,
            }))
            return
          }
          ws.send(JSON.stringify({
            op: 'start_run',
			protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
            request_id: requestId,
            session_id: currentSessionId,
            create_by: options?.createBy ?? undefined,
            model_id: options?.modelId ?? undefined,
            message: {
              content: userMessage,
              role: 'user',
              attachments: attachmentIds,
              mention_images: mentionImagePaths,
              ...(options?.choice ? { choice: options.choice } : {}),
              ...(options?.skillName ? { skill_name: options.skillName } : {}),
              ...(options?.skillPath ? { skill_path: options.skillPath } : {}),
            },
          }))
        }
        ws.onerror = () => {
          clearSocket()
          if (terminal) resolve()
          else reject(new Error('WebSocket error'))
        }
        ws.onclose = () => {
          clearSocket()
          streamAbortController?.signal.removeEventListener('abort', onAbort)
          if (terminal) resolve()
          else reject(new Error(opened ? 'Stream disconnected before completion' : 'WebSocket connection failed'))
        }
        ws.onmessage = (event) => {
          try {
            const rawMsg = JSON.parse(String(event.data)) as Record<string, unknown>
            const op = String(rawMsg.op ?? '').trim()
			const hello = parseForebrainGatewayServerHello(rawMsg)
			if (hello?.protocolVersion && hello.protocolVersion !== FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION) {
			  try { ws.close() } catch { /* noop */ }
			  reject(new Error(`Gateway protocol mismatch (client ${FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION}, server ${hello.protocolVersion})`))
			  return
			}
            const msg = toCamelCase(rawMsg) as Record<string, unknown>
            const sessionBound = parseForebrainSessionBoundMessage(rawMsg)
            if (sessionBound) {
              const result = handleOp('session_bound', {
                sessionId: sessionBound.sessionId,
                requestId: sessionBound.requestId,
                message: sessionBound.message,
                sessionSwitched: sessionBound.sessionSwitched,
              })
              if (result?.terminal) {
                terminal = true
                try {
                  ws.close()
                } catch {
                  //
                }
              }
              return
            }
            const mcpStatusMsg = parseForebrainMcpStatusMessage(rawMsg)
            if (mcpStatusMsg) {
              mcpStatus.value = mcpStatusMsg
              return
            }
            const runEvent = parseForebrainRunEventMessage(rawMsg)
            if (runEvent) {
			  if (!isSupportedForebrainRunEventSchema(runEvent.schemaVersion)) {
				activeRunId = null
				try { ws.close() } catch { /* noop */ }
				reject(new Error(`Unsupported session event schema ${runEvent.schemaVersion}; this client supports ${FOREBRAIN_RUN_EVENT_SCHEMA_VERSION}`))
				return
			  }
              if (AUTO_CONTINUE_EVENT_TYPES.has(String(runEvent.type))) {
                if (rememberObservedEvent(runEvent)) applyAutoContinueEvent(runEvent, false)
                return
              }
              if (String(runEvent.type) === 'heartbeat_fired') {
                if (rememberObservedEvent(runEvent)) applyHeartbeatFiredEvent(runEvent, false)
                return
              }
              if (String(runEvent.type) === LSP_RECOMMENDATION_EVENT_TYPE) {
                if (rememberObservedEvent(runEvent)) {
                  const sequence = Number(runEvent.sequence ?? 0)
                  if (!(sequence > 0 && sequence <= observerHighWater)) {
                    lspRecommendation.value = parseLspRecommendation(runEvent.payload)
                  }
                }
                return
              }
              // Primary canonical events mirror the legacy operations on this
              // same request socket. Project only child events here; retaining
              // the canonical id/cursor prevents the observer from replaying
              // the primary event after streaming finishes.
              if (resume) {
				const eventRunId = String(runEvent.runId ?? '').trim()
				if (eventRunId && eventRunId !== String(activeRunId ?? '').trim() && !isSubagentHistoryEvent(runEvent)) {
				  // resume_connection observes a conversation-level durable tail.
				  // Keep an unrelated primary run out of this request's assistant
				  // projection; the ordinary observer reducer applies it once the
				  // active send is no longer mutating shared runtime state.
				  applyObservedEvent(runEvent)
				} else {
				  const projection = eventProjection(runEvent)
				  handleRunEvent(runEvent, projection.assistantMessageId, projection.planBlocks, projection.state)
				}
			  } else if (!legacyAlreadyProjected(runEvent)) {
				handleRunEvent(runEvent, assistantMessageId, planBlocksAccumulator, eventState)
              } else {
                rememberObservedEvent(runEvent)
              }
              // A run the gateway still drives on this socket ends with its
              // legacy frame, which follows the event's mirror. One it no
              // longer drives here — resumed after an approval, or rejoined
              // after a disconnect — reports its end only as this event.
              const runOfThisSend = String(runEvent.runId ?? '').trim() === String(activeRunId ?? '').trim()
              if (
				runOfThisSend &&
				isTerminalRunEventType(String(runEvent.type)) &&
				(resume || !legacyMirroredRunIDs.has(String(runEvent.runId ?? '').trim()))
			  ) {
                terminal = true
                try {
                  ws.close()
                } catch {
                  //
                }
              }
              return
            }
            const result = handleOp(op, msg)
			if (result?.terminal || op === 'run_completed' || op === 'run_error' || op === 'run_cancelled') {
              terminal = true
              try {
                ws.close()
              } catch {
                //
              }
            }
          } catch (ex) {
            reject(ex)
          }
        }
      })
      const maxReconnectAttempts = 5
      for (let attempt = 0; ; attempt++) {
        try {
          await connectOnce(attempt > 0, attempt)
          break
        } catch (e) {
          if (!activeRunId || attempt >= maxReconnectAttempts) {
            throw e
          }
          updateRuntimeKind('reconnecting', attempt + 1)
          await new Promise((resolve) => setTimeout(resolve, Math.min(1000 * (attempt + 1), 5000)))
          updateRuntimeKind('working')
        }
      }
      const currentAssistant = messages.value.find((message) => message.id === answerId())
      const hasAssistantData = Boolean(eventState.fullAnswer) ||
		Boolean(currentAssistant?.picker) ||
		Boolean(currentAssistant?.blocks?.length) ||
        planBlocksAccumulator.length > 0 ||
		Boolean(currentAssistant?.planUpdates?.length)
      // A run that ended — finished, failed or stopped — closes with its
      // worked line even when it said nothing, the way the terminal closes
      // every run; the turn stays to carry that line.
      const runEnded = currentAssistant?.workedDurationMs != null
      if (!hasAssistantData && !runEnded) {
        messages.value = messages.value.filter((message) => message.id !== answerId())
      } else {
        updateMessageById(answerId(), (message) => ({
          ...message,
          // The timeline is what the turn said, in the order it said it; the raw
          // delta stream is only a fallback for a turn that produced no blocks
          // at all. A turn that said nothing has no text, not a placeholder.
          content: spokenText(message.blocks ?? []) || eventState.fullAnswer || (message.planUpdates?.length || message.content === NOTHING_SAID_YET ? '' : message.content),
          plan: planBlocksAccumulator.length > 0 ? planBlocksAccumulator[planBlocksAccumulator.length - 1].plan : plan.value ?? null,
          planBlocks: planBlocksAccumulator.length > 0 ? planBlocksAccumulator.map((b) => ({ plan: b.plan })) : undefined,
        }))
      }
      answer.value = ''
      plan.value = null
    } catch (e) {
      const isAbort = (e as { name?: string })?.name === 'AbortError'
      if (isAbort) {
        const finalContent = answer.value?.trim() ? answer.value : translate('chat.cancelled')
        updateMessageById(answerId(), (message) => ({
          ...message,
          content: finalContent,
        }))
      } else {
        setError(getErrorMessage(e))
	const partial = messages.value.find((message) => message.id === answerId())
	if (!partial?.blocks?.length && !partial?.content?.trim()) {
		  messages.value = messages.value.filter((message) => message.id !== answerId())
		}
      }
    } finally {
      // Only the send that still owns the global stream state clears it. A
      // later send, or a session reset, has replaced the controller, and
      // clearing the state here would end that run's status line, unlock the
      // composer early, and leave cancel() nothing to act on.
      if (streamAbortController === myController) {
        isStreaming.value = false
        clearRuntimeStatus()
        streamAbortController = null
        activeRunId = null
        contextSignals.value = { ...contextSignals.value, activeRunId: undefined }
      }
	  flushBufferedSessionEvents()
    }
    // This send's end runs what its run handed back as its next turn — the
    // engine already decided that, listing the messages in next — merged into
    // one message, followed by anything still held for a run that never
    // started. A withdrawn send takes them back to the composer after itself.
    eventState.awaitingRelease = false
    const released = await Promise.all((eventState.released ?? []).map(releasedSubmission))
    const releasedAsOne = released.length
      ? {
          text: released.map((submission) => submission.text).join('\n\n'),
          attached: released.reduce((merged, submission) => mergeSubmissions(merged, submission)),
        }
      : null
    const following: HeldSend[] = [
      ...(releasedAsOne ? [{
        text: releasedAsOne.text,
        options: { sessionId: runSession || undefined, modelId: options?.modelId, createBy: options?.createBy, attached: releasedAsOne.attached },
        settle: () => {},
      }] : []),
      ...heldBeforeRun.value,
    ]
    heldBeforeRun.value = []
    if (withdrawn) giveBack(submissionOf(userMessage, options), withdrawnError, withdrawnCode)
    // What waited on this send belongs to the conversation it ran in. A
    // composer now viewing another one takes the messages back — the same
    // rule a session reset applies to what it holds — instead of running
    // them as a turn of the conversation on screen.
    if (withdrawn || sessionId.value !== runSession) {
      giveBackHeld(following)
      return
    }
    const [next, ...rest] = following
    if (!next) return
    heldBeforeRun.value = rest
    await send(next.text, next.options)
    next.settle()
  }

  /**
   * The message holding a compaction's card. A compaction has one card for its
   * whole life, whichever socket delivers its next event.
   */
  function compactionHostId(compactionId: string): string | undefined {
    const host = messages.value.find((message) => message.blocks?.some(
      (block) => block.kind === 'compaction' && block.compaction.compactionId === compactionId,
    ))
    return host?.id ? String(host.id) : undefined
  }

  function runningCompaction(): boolean {
    return messages.value.some((message) => message.blocks?.some(
      (block) => block.kind === 'compaction' && block.compaction.status === 'running',
    ))
  }

  function cancel() {
    // A compaction running before any run exists — a /compact the user asked
    // for, or the one a message waits on before its turn begins — has no run
    // to cancel: the stop goes to the send on the same socket, which reports
    // the compaction cancelled and, for a waiting message, withdraws it.
    if (!activeRunId && activeSocket && activeSocket.readyState === WebSocket.OPEN && runningCompaction()) {
      activeSocket.send(JSON.stringify({ op: 'cancel_command', protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION }))
      return
    }
    const runId = activeRunId?.trim()
    if (runId) {
      forebrainApi.runCancel(runId)
        .then((resp) => {
          if (resp.preview) applyPendingInputPreview(resp.preview)
        })
        .catch(() => {
          if (streamAbortController) streamAbortController.abort()
        })
      return
    }
    if (streamAbortController) streamAbortController.abort()
    if (activeSocket) {
      try {
        activeSocket.close()
      } catch {
        //
      }
    }
  }

  // Esc while messages wait on the run: the engine owns what an interrupted
  // run hands back — the steers queued behind it follow as its next turn, the
  // rest returns to the composer — so this page only cancels and obeys the
  // release it gets back.
  function sendPendingSteersAfterInterrupt() {
    cancel()
  }

  async function loadMessages(sid: string): Promise<void> {
	const target = sid.trim()
	if (!target) return
    const generation = historyGeneration
    const snapshotEpoch = sendEpoch
    historyLoadingSession = target
	if (generation === historyGeneration && sessionId.value === target) {
	  historyLoading.value = true
	  historyError.value = null
	}
    try {
	  // Observation starts before the snapshot requests. Incoming events stay in
	  // bufferedSessionEvents until every snapshot has been projected, closing
	  // the history/tail race without delaying the ordinary transcript request.
	  void startSessionObserver(target)
	  const eventHistory = (async () => {
		const events: ForebrainRunEvent[] = []
		let cursor = 0
		let highWater: number | undefined
		let error: unknown = null
		try {
		  for (;;) {
			const page = await forebrainApi.sessionEvents(target, { cursor, highWater, limit: 1000 })
			if (!isSupportedForebrainRunEventSchema(Number(page.schemaVersion))) {
			  throw new Error(`Unsupported session event schema ${page.schemaVersion}; this client supports ${FOREBRAIN_RUN_EVENT_SCHEMA_VERSION}`)
			}
			if (highWater == null) highWater = Number(page.highWater ?? 0)
			const pageEvents = Array.isArray(page.events) ? page.events : []
			const unsupported = pageEvents.find((evt) => !isSupportedForebrainRunEventSchema(evt.schemaVersion))
			if (unsupported) {
			  throw new Error(`Unsupported session event schema ${unsupported.schemaVersion}; this client supports ${FOREBRAIN_RUN_EVENT_SCHEMA_VERSION}`)
			}
			events.push(...pageEvents)
			const next = Number(page.nextCursor ?? cursor)
			if (!page.hasMore || next <= cursor) break
			cursor = next
		  }
		} catch (loadError) {
		  error = loadError
		}
		return { events, highWater, error }
	  })()
	  const legacyHistory = forebrainApi.sessionSubagentHistory(target)
		.then((value) => ({ value, error: null as unknown }))
		.catch((error: unknown) => ({ value: { sessionId: target, records: [] }, error }))
	  const [{ events, highWater, error: eventHistoryError }, list, legacySubagents] = await Promise.all([
		eventHistory,
		forebrainApi.chatMessages(target, 0),
		legacyHistory,
	  ])
	  // A 404 is the explicit compatibility signal for a pre-event-log server.
	  // Treating every transport/500 failure as legacy produces an apparently
	  // successful replay with every canonical subagent/tool/approval block
	  // silently missing.
	  if (eventHistoryError && responseStatus(eventHistoryError) !== 404) throw eventHistoryError
	  if (events.length === 0 && legacySubagents.error && responseStatus(legacySubagents.error) !== 404) {
		throw legacySubagents.error
	  }
	  if (generation !== historyGeneration || (sessionId.value != null && sessionId.value !== target)) return
      const transcript = conversationFromTranscript(Array.isArray(list) ? list : [], shouldRenderPlan)
      const streamedIntoTarget = (lastSendEpochBySession.get(target) ?? 0) > snapshotEpoch
      if (streamedIntoTarget) {
        // A send into this same session started after the snapshot was
        // requested: the local view is newer than the snapshot, which
        // predates the turn's persistence. Drop it; the observer's durable
        // events already painted the same facts, and the next snapshot (with
        // the persisted rows) reconciles.
        return
      }
      if (isStreaming.value) {
        // A send that started before this snapshot resolved owns the live
        // tail; replacing the list under it would erase the turn the user is
        // watching. The observer's durable events already carry the same
        // facts, so the local turn bubbles ride below the transcript until
        // the stream closes and the next snapshot reconciles.
        messages.value = [...transcript, ...messages.value.filter((m) => m.role === 'user' || m.role === 'assistant')]
      } else {
        messages.value = transcript
      }
	  subagents.value = []
	  eventProjectionByRun.clear()
	  appliedEventIDs.clear()
	  clearSubagentCardState()
	  // The transcript's own rows carry the calls' facts; a replayed spawn
	  // binds to its dispatching card through them, so they are indexed
	  // before the events are applied.
	  for (const message of messages.value) {
		for (const block of message.blocks ?? []) {
		  if (block.kind === 'tool') rememberSubagentCallStep(block.step)
		}
	  }
	  for (const evt of events.sort((a, b) => Number(a.sequence ?? 0) - Number(b.sequence ?? 0))) {
		applyObservedEvent(evt, true)
	  }
	  sessionEventCursor = Math.max(sessionEventCursor, Number(highWater ?? 0))
	  // Sessions created before the canonical event log retain their ledger
	  // summary as an explicitly best-effort transcript. New sessions always
	  // take the lossless event path above.
	  if (subagents.value.length === 0) {
		for (const record of legacySubagents.value.records ?? []) {
		  const agentId = String(record.agentId ?? record.taskId ?? record.runId ?? '').trim()
		  if (!agentId) continue
		  const status = String(record.status ?? '').toLowerCase()
		  const startedAt = Number(record.startedAt)
		  const finishedAt = Number(record.finishedAt)
		  const executionId = String(record.executionId ?? record.runId ?? '').trim() || agentId
		  const stepId = `subagent-exec:${executionId}`
		  updateSubagent(agentId, (entry) => ({
			...entry,
			agentType: 'subagent',
			task: String(record.task ?? ''),
			status: status === 'cancelled' ? 'cancelled' : status === 'failed' ? 'failed' : status === 'running' ? 'running' : 'done',
			error: String(record.error ?? '').trim() || undefined,
			blocks: [
			  ...(record.task ? [{ kind: 'prompt' as const, text: String(record.task) }] : []),
			  ...(record.output ? [{ kind: 'assistant' as const, text: String(record.output), open: false }] : []),
			],
		  }))
		  // The ledger record is an execution with no dispatching call: one
		  // card of its own in the conversation, the same card a live
		  // execution draws, with the record's own clock.
		  const projection = eventProjection({ runId: record.runId, type: 'legacy_subagent' })
		  if (projection.assistantMessageId) {
			updateMessageById(projection.assistantMessageId, (message) => (
			  (message.blocks ?? []).some((b) => b.kind === 'subagent' && b.id === stepId)
				? message
				: {
				  ...message,
				  blocks: [...(message.blocks ?? []), {
					kind: 'subagent' as const,
					id: stepId,
					stepId,
					step: {
					  stepId,
					  toolName: '',
					  summary: firstLine(String(record.task ?? '')) || agentId,
					  status: status === 'running' ? 'running' : 'completed',
					  subagentCall: {
						verb: 'run',
						tasks: [{
						  index: 0,
						  key: agentId,
						  title: firstLine(String(record.task ?? '')),
						  agentType: 'subagent',
						  status: status === 'cancelled' ? 'cancelled' : status === 'failed' ? 'failed' : status === 'running' ? 'running' : 'done',
						  error: String(record.error ?? '').trim() || undefined,
						  executionId,
						  startedAt: Number.isFinite(startedAt) && startedAt > 0 ? startedAt : undefined,
						  finishedAt: Number.isFinite(finishedAt) && finishedAt > 0 ? finishedAt : undefined,
						}],
					  },
					} as SubagentToolStep,
				  }],
				}
			))
			subagentCallStepIds.add(stepId)
		  }
		}
	  }
    } catch (loadError) {
	  // Keep already rendered history. A reconnect or manual retry can fill the
	  // missing page without blanking the session the user was reading.
	  if (generation === historyGeneration && sessionId.value === target) {
		historyError.value = getErrorMessage(loadError)
	  }
	} finally {
	  if (generation === historyGeneration && historyLoadingSession === target) {
		historyLoadingSession = ''
		historyLoading.value = false
		flushBufferedSessionEvents()
	  }
    }
  }

  function switchToSession(sid: string) {
    applySessionReset({ sid, loadMessages: true, loadMode: true })
  }

  function resetForNewChat() {
    applySessionReset({ sid: null, loadMessages: false, loadMode: false })
  }

  // Returns the full listing, provider status included: a ChatGPT discovery
  // failure (logged out, rate limited) must reach callers as status, not as a
  // silently empty model list.
  async function fetchModels(): Promise<ModelCatalogListing> {
    try {
      return await forebrainApi.getModels()
    } catch {
      return { records: [], status: [] }
    }
  }

  return {
    sessionId,
    mode,
    modePhase,
    plan,
    answer,
    isStreaming,
    error,
    messages,
    choose,
	historyLoading,
	historyError,
    subagents,
    subagentCallTasks,
    subagentNow,
    subagentPendingInput,
    subagentReturnedDraft,
    takeSubagentReturnedDraft,
    sendToSubagent,
    recallSubagentInput,
    interruptSubagentToSend,
    withdrawSubagentInput,
    compactSubagent,
    loadSubagentContext,
    loadSubagentBudget,
    pendingInputPreview,
    returnedDraft,
    takeReturnedDraft,
    takeReturnedNotice,
    takeReturnedNoticeCode,
    runtimeStatus,
    mcpStatus,
    autoContinue,
    subagentAutoContinue,
    autoContinueForView,
    cancelAutoContinue,
    lspRecommendation,
    contextSignals,
    pendingActionsVersion,
    send,
    editLastQueuedMessage,
    sendPendingSteersAfterInterrupt,
    cancel,
    fetchModels,
    loadMessages,
    switchToSession,
    resetForNewChat,
    computeWorkedDurationMs,
    formatWorkedDurationLabel,
    formatRuntimeStatusLabel,
  }
}
