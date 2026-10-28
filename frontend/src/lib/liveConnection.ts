import { reactive } from 'vue'

/**
 * Whether a conversation's observation socket is carrying anything.
 *
 * A conversation page draws the engine's own frames: the state of a run, the
 * words of an answer still being written. The socket that carries them can
 * drop, and a page that keeps drawing the last frame it received looks exactly
 * like a page whose run has stopped moving. This module is where the socket's
 * own state is kept for the views, one entry per conversation — the same key
 * `batchExecution` uses, for the same reason: the socket belongs to the
 * conversation's stream, and the page that says "we are not hearing anything"
 * is the stream's parent, not the stream.
 *
 * A reconnect replays the durable tail from the bind's cursor, so the state
 * heals itself; what a page owes the person meanwhile is the truth about the
 * connection, not a second way to read the run.
 */
export type LiveState = 'live' | 'reconnecting' | 'offline'

const states = reactive(new Map<string, LiveState>())

/** Records what one conversation's observation socket is doing. */
export function setLiveState(sessionId: string, state: LiveState): void {
  const sid = sessionId.trim()
  if (!sid) return
  if (state === 'live') states.delete(sid)
  else states.set(sid, state)
}

/**
 * What this conversation's observation socket is doing. Unknown reads as
 * `live`: nothing has gone wrong yet, and a page opened a moment before its
 * socket bound must not announce a pause that is not happening.
 */
export function liveStateFor(sessionId: string): LiveState {
  return states.get(sessionId.trim()) ?? 'live'
}
