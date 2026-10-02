import { ref } from 'vue'

const LAST_SESSION_KEY = 'forebrain_last_session_id'

const lastSessionId = ref<string | null>(null)

try {
  if (typeof localStorage !== 'undefined') {
    lastSessionId.value = localStorage.getItem(LAST_SESSION_KEY)
  }
} catch {
  // Storage unavailable: the value simply does not survive a reload.
}

/**
 * The conversation the browser was last on, for a view that needs to ask about
 * a session it does not own — the MCP page reports the servers of the Runner
 * that session runs on, which is not necessarily the primary agent's.
 */
export function lastSessionIdValue(): string {
  return String(lastSessionId.value ?? '').trim()
}

export function persistLastSessionId(sid: string | null) {
  lastSessionId.value = sid
  try {
    if (typeof localStorage === 'undefined') return
    if (sid && sid.trim()) {
      localStorage.setItem(LAST_SESSION_KEY, sid.trim())
    } else {
      localStorage.removeItem(LAST_SESSION_KEY)
    }
  } catch {
    //
  }
}

export function useLastSession() {
  return { lastSessionId }
}
