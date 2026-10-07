import { ref } from 'vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { t } from '@/locales'

export interface ChatSessionItem {
  id: string
  title: string | null
  createTime: string
  updateTime: string
  /** What the session is for: '' ordinary, 'workshop' a skill-workshop task. */
  source: string
}

/**
 * The primary agent's conversation list, as one shared fact: the rail's
 * drawer lists it, a finished run refreshes it, and the chat page watches
 * it as the signal to re-read the open conversation. Module-level state,
 * like every tenant-scoped store here.
 */
const sessions = ref<ChatSessionItem[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

async function fetchSessions() {
  loading.value = true
  error.value = null
  try {
    const data = await forebrainApi.chatSessions('')
    const records = data?.records ?? []
    sessions.value = records.map((r) => ({
        id: r.id,
        title: r.title || t('chatDrawer.untitled'),
        createTime: r.createTime,
        updateTime: r.updateTime,
        source: r.source ?? '',
      }))
  } catch (e) {
    sessions.value = []
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

/**
 * Open a new conversation: create it, refresh the list, and hand back the
 * id. A failure is the caller's to show — nothing here resets silently.
 */
async function createSession(): Promise<string> {
  const created = await forebrainApi.chatSessionCreate()
  await fetchSessions()
  return created.id
}

export function useChatSessions() {
  return { sessions, loading, error, fetchSessions, createSession }
}

import { onTenantReset } from '@/composables/useTenantScope'

// Sessions belong to the tenant: a switch empties the list until the next
// fetch, so no previous agent's conversation can appear under the new one.
onTenantReset(() => {
  sessions.value = []
  loading.value = false
  error.value = null
})
