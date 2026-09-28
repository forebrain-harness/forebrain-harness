import { ref } from 'vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { t } from '@/locales'

export interface ChatSessionItem {
  id: string
  title: string | null
  createTime: string
  updateTime: string
}

export function useChatSessions() {
  const sessions = ref<ChatSessionItem[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function fetchSessions() {
    loading.value = true
    error.value = null
    try {
      const data = await forebrainApi.chatSessions(1, 100)
      const records = data?.records ?? []
      sessions.value = records.map((r) => ({
        id: r.id,
        title: r.title || t('sidebar.untitledChat'),
        createTime: r.createTime,
        updateTime: r.updateTime,
      }))
    } catch (e) {
      sessions.value = []
      error.value = getErrorMessage(e)
    } finally {
      loading.value = false
    }
  }

  return {
    sessions,
    loading,
    error,
    fetchSessions,
  }
}
