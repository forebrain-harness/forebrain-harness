import { forebrainApi } from '@/lib/api'
import { refusalCodeOf } from '@/lib/tasks'
import { FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION, buildBrowserForebrainGatewayChatWsUrl } from '@/lib/forebrainGatewayRuntime'

/**
 * Driving a batch execution from the task pages (plan 153).
 *
 * The web may not talk to a batch task — it may only start, stop and restart
 * its runs. Starting one is the same `start_run` op the conversation page sends,
 * on a socket this module opens and closes itself: a batch turn is unattended
 * once it starts, so the run outlives the connection that asked for it. The
 * engine replaces an empty message with what the execution runs on (the task's
 * fixed input, or the continue prompt of a stopped one), so "continue" and
 * "run again" both send nothing at all.
 */

/** A turn the engine refused, carrying the stable code a caller switches on. */
export class BatchStartRefusal extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message || code)
    this.name = 'BatchStartRefusal'
    this.code = code
  }
}

export interface BatchRunStart {
  /** The task's own words for its first execution; empty starts on the input
   *  the task already carries. */
  content?: string
  /** Uploaded file ids the first execution takes as its inputs. */
  attachments?: string[]
  /** How long to wait for the engine to answer the request, in milliseconds. */
  timeoutMs?: number
}

/**
 * Starts one turn of the conversation's task execution and resolves with the
 * run it opened. The socket is closed as soon as the run exists: the turn keeps
 * running on the server, which is exactly what a viewer of a task wants.
 */
export function startBatchRun(sessionId: string, options: BatchRunStart = {}): Promise<string> {
  const sid = String(sessionId ?? '').trim()
  if (!sid) return Promise.reject(new Error('a batch run needs a conversation'))
  const timeoutMs = options.timeoutMs ?? 20000
  return new Promise<string>((resolve, reject) => {
    let settled = false
    const socket = new WebSocket(buildBrowserForebrainGatewayChatWsUrl())
    const finish = (fn: () => void) => {
      if (settled) return
      settled = true
      window.clearTimeout(timer)
      try {
        socket.close()
      } catch {
        // A socket that never opened has nothing to close.
      }
      fn()
    }
    const timer = window.setTimeout(() => {
      finish(() => reject(new Error('the gateway did not answer the start request')))
    }, timeoutMs)
    socket.onopen = () => {
      socket.send(JSON.stringify({
        op: 'start_run',
        protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
        request_id: `task-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
        session_id: sid,
        message: {
          content: String(options.content ?? ''),
          role: 'user',
          attachments: options.attachments ?? [],
        },
      }))
    }
    socket.onerror = () => finish(() => reject(new Error('WebSocket error')))
    socket.onclose = () => finish(() => reject(new Error('the connection closed before the run started')))
    socket.onmessage = (message) => {
      let payload: Record<string, unknown>
      try {
        payload = JSON.parse(String(message.data)) as Record<string, unknown>
      } catch {
        return
      }
      const op = String(payload.op ?? '').trim()
      // A run the engine would not open says why with the protocol's own code
      // (batch_execution_running, batch_execution_finished, …).
      if (op === 'turn_withdrawn' || op === 'run_error') {
        finish(() => reject(new BatchStartRefusal(refusalCodeOf(payload.data), String(payload.error ?? ''))))
        return
      }
      if (op === 'run_started') {
        finish(() => resolve(String(payload.runId ?? '').trim()))
      }
    }
  })
}

/**
 * Stops the execution's running turn: the roster answers which run the
 * conversation's primary agent is working on right now, and the gateway stops
 * that run the way the terminal's own stop does. Resolves false when nothing
 * was running — the execution ended on another surface first.
 */
export async function stopBatchRun(sessionId: string): Promise<boolean> {
  const sid = String(sessionId ?? '').trim()
  if (!sid) return false
  const roster = await forebrainApi.agentRoster(sid)
  const row = (roster?.records ?? []).find(
    (record) => record.kind === 'primary' && String(record.runId ?? '').trim() !== ''
      && String(record.status ?? '').trim().toLowerCase() === 'running',
  )
  const runId = String(row?.runId ?? '').trim()
  if (!runId) return false
  await forebrainApi.runCancel(runId)
  return true
}
