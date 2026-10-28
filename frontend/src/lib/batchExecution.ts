import { reactive } from 'vue'

/**
 * A task execution's state, as the engine reports it.
 *
 * The state of a batch execution is the engine's own fact — it is what wrote
 * the row, the only thing that admits a turn and the only thing that settles
 * one — so the page showing a running execution is told it rather than asking
 * the task every few seconds. The engine publishes one `batch_execution` frame
 * each time an execution moves (it started running, it ended in some status),
 * the frame is durable, and every page watching the conversation receives it:
 * live, and again from the snapshot a page is handed when it binds. This module
 * is where those frames are kept for the views, one entry per conversation.
 *
 * It is keyed by session rather than held per conversation instance because
 * the frames arrive through the conversation's stream while the page that draws
 * a run — TaskRunView — is its parent: the two are different components, and
 * the state they share is the conversation's, not the stream's.
 */

/** One execution's state as the last frame said it. */
export interface BatchExecutionState {
  taskId: string
  sessionId: string
  /** Which execution of the task this is; 0 when the frame did not say. */
  seq: number
  /** `pending` | `running` | `stopped` | `completed` | `failed`. */
  status: string
  errorCode: string
  errorText: string
  /** The execution's own clock, in unix seconds; 0 when not yet known. */
  startedAt: number
  finishedAt: number
  /** The durable cursor of the frame that said this. */
  sequence: number
}

const executions = reactive(new Map<string, BatchExecutionState>())
const binds = reactive(new Map<string, number>())

function asNumber(value: unknown): number {
  const n = Number(value)
  return Number.isFinite(n) ? n : 0
}

function asText(value: unknown): string {
  return String(value ?? '').trim()
}

/**
 * recordBatchExecution files one `batch_execution` frame.
 *
 * Frames are applied in the order the log recorded them, not the order they
 * were delivered: a replay overlaps what is already known, and a page that
 * reconnects can be handed a state it has already moved past. A frame older
 * than the one held is therefore dropped — the execution's state only ever
 * moves forward — while a frame that carries no cursor (nothing to order by)
 * is taken as the newest, which is what the live path is.
 */
export function recordBatchExecution(frame: {
  sessionId?: unknown
  payload?: Record<string, unknown> | undefined
  sequence?: unknown
}): void {
  const payload = frame.payload ?? {}
  const sessionId = asText(payload.sessionId) || asText(frame.sessionId)
  const status = asText(payload.status)
  if (!sessionId || !status) return
  const sequence = asNumber(frame.sequence)
  const held = executions.get(sessionId)
  if (held && sequence > 0 && held.sequence > sequence) return
  executions.set(sessionId, {
    taskId: asText(payload.taskId),
    sessionId,
    seq: asNumber(payload.seq),
    status,
    errorCode: asText(payload.errorCode),
    errorText: asText(payload.errorText),
    startedAt: asNumber(payload.startedAt),
    finishedAt: asNumber(payload.finishedAt),
    sequence,
  })
}

/** The last state a frame reported for this conversation, or null if none has. */
export function liveBatchExecution(sessionId: string): BatchExecutionState | null {
  return executions.get(sessionId.trim()) ?? null
}

/**
 * noteSessionBound counts one bind of a conversation's observation socket —
 * the first attach and every reconnect after it.
 *
 * It exists for the one case frames cannot cover: a page that binds and is
 * handed a snapshot with no execution frame in it (an execution opened before
 * this engine published them, or a state whose frames were lost) has nothing to
 * be driven by, and a reconnect is the moment it may read once to catch up.
 * Counting is what lets the page tell its first attach from a later one, so it
 * never re-reads the facts it has just read.
 */
export function noteSessionBound(sessionId: string): void {
  const sid = sessionId.trim()
  if (!sid) return
  binds.set(sid, (binds.get(sid) ?? 0) + 1)
}

/** How many times this conversation's observation socket has bound. */
export function sessionBindCount(sessionId: string): number {
  return binds.get(sessionId.trim()) ?? 0
}
