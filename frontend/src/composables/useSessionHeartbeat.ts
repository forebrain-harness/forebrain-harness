import { ref } from 'vue'
import { getErrorMessage, forebrainApi, type HeartbeatRecord } from '@/lib/api'

/**
 * The current session's heartbeat, as one shared fact: the rail's popover
 * edits it and the chat drawer's session row reads whether one is enabled.
 */
const current = ref<HeartbeatRecord | null>(null)

export function useSessionHeartbeat() {
  async function load(sessionId: string | null) {
    const sid = String(sessionId ?? '').trim()
    current.value = null
    if (!sid) return
    try {
      current.value = await forebrainApi.heartbeat(sid)
    } catch {
      // The drawer's indicator stays silent on a load failure; the popover
      // shows the error when it is open and retrying.
    }
  }

  async function save(input: { sessionId: string; intervalSeconds: number; prompt: string; paused: boolean }) {
    current.value = await forebrainApi.saveHeartbeat(input)
    return current.value
  }

  async function clear(sessionId: string) {
    await forebrainApi.clearHeartbeat(sessionId)
    current.value = null
  }

  function errorMessage(error: unknown): string {
    return getErrorMessage(error)
  }

  return { current, load, save, clear, errorMessage }
}
