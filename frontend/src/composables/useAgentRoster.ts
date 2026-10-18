import { readonly, ref } from 'vue'
import type { RouteLocationRaw } from 'vue-router'
import { forebrainApi, type AgentRosterRow } from '@/lib/api'

export function agentRosterViewTarget(row: AgentRosterRow): RouteLocationRaw | null {
  const sessionId = String(row.sessionId ?? '').trim()
  if (!sessionId) return null
  return { path: '/', query: { session: sessionId } }
}

/** The conversation a row ran in: a subagent's parent conversation, or the
 * row's own session for a primary agent. */
export function agentRosterConversationTarget(row: AgentRosterRow): RouteLocationRaw | null {
  const sessionId = String(row.parentSessionId ?? row.sessionId ?? '').trim()
  if (!sessionId) return null
  return { path: '/', query: { session: sessionId } }
}

export function useAgentRoster() {
  const records = ref<AgentRosterRow[]>([])
  const loading = ref(false)
  const error = ref('')

  async function loadRoster() {
    loading.value = true
    error.value = ''
    try {
      const res = await forebrainApi.agentRoster()
      records.value = Array.isArray(res.records) ? res.records : []
    } catch (e) {
      records.value = []
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  async function cancelRoster(row: AgentRosterRow) {
    if (row.kind === 'primary') {
      await forebrainApi.primaryAgentCancel(row.id)
    } else {
      await forebrainApi.subagentCancel(row.id)
    }
    await loadRoster()
  }

  async function cancelAll() {
    await forebrainApi.agentsCancelAll()
    await loadRoster()
  }

  return {
    records: readonly(records),
    loading: readonly(loading),
    error: readonly(error),
    loadRoster,
    cancelRoster,
    cancelAll,
  }
}
