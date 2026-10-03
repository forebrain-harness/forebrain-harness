import { computed, ref } from 'vue'
import { persistLastSessionId } from '@/composables/useLastSession'
import { forebrainApi, getErrorMessage, type PrimaryAgentRecord } from '@/lib/api'

const records = ref<PrimaryAgentRecord[]>([])
const activeId = ref('')
const loading = ref(false)
const error = ref('')
const refreshToken = ref(0)

export function usePrimaryAgents() {
  const active = computed(() => records.value.find((item) => item.id === activeId.value) || records.value[0] || null)

  async function loadPrimaryAgents() {
    loading.value = true
    error.value = ''
    try {
      const res = await forebrainApi.primaryAgents()
      records.value = Array.isArray(res.records) ? res.records : []
      activeId.value = res.activeId || records.value.find((item) => item.active)?.id || records.value[0]?.id || ''
    } catch (e) {
      error.value = getErrorMessage(e)
    } finally {
      loading.value = false
    }
  }

  async function switchPrimaryAgent(id: string) {
    loading.value = true
    error.value = ''
    const before = activeId.value
    try {
      const res = await forebrainApi.primaryAgentSwitch(id)
      records.value = Array.isArray(res.records) ? res.records : []
      activeId.value = res.activeId
      // A primary agent is a tenant: its sessions, workspace, skills and
      // memories are its own. The remembered session belongs to the agent we
      // just left, so it is dropped rather than reopened under the new one.
      persistLastSessionId(null)
      refreshToken.value += 1
    } catch (e) {
      const message = getErrorMessage(e)
      // A switch records its target before it rebuilds the runtime, and a
      // failed rebuild leaves the gateway on that target, refusing work until
      // it is repaired. The page shows the tenant the gateway is actually on
      // rather than the one it was on before asking.
      await loadPrimaryAgents()
      if (activeId.value && activeId.value !== before) {
        persistLastSessionId(null)
        refreshToken.value += 1
      }
      error.value = message
      throw e
    } finally {
      loading.value = false
    }
  }

  /**
   * Takes in a switch made elsewhere — /agent in the chat — the way a switch
   * made here is taken in.
   */
  async function adoptActivePrimaryAgent() {
    const before = activeId.value
    await loadPrimaryAgents()
    if (activeId.value && activeId.value !== before) {
      persistLastSessionId(null)
      refreshToken.value += 1
    }
  }

  return {
    adoptActivePrimaryAgent,
    records,
    activeId,
    active,
    loading,
    error,
    refreshToken,
    loadPrimaryAgents,
    switchPrimaryAgent,
  }
}
