import { ref } from 'vue'
import { getErrorMessage, forebrainApi, type HeartbeatRecord } from '@/lib/api'
import { onTenantReset } from '@/composables/useTenantScope'

/**
 * The agent's heartbeats, as one shared fact: the chat page's heartbeat
 * control edits the open conversation's and lists the others, and the chat
 * drawer marks every conversation that has one. A heartbeat keeps asking a
 * conversation again while nobody looks at it, so the list is what keeps a
 * forgotten one visible.
 */
const records = ref<HeartbeatRecord[]>([])
// Bumped by every write and reset: a list read that began before it is older
// than what the page now holds, and is dropped when it lands.
let generation = 0

async function refresh() {
  const started = ++generation
  const list = await forebrainApi.heartbeats()
  if (started === generation) records.value = list
}

function forSession(sessionId: string | null): HeartbeatRecord | null {
  const sid = String(sessionId ?? '').trim()
  if (!sid) return null
  return records.value.find((record) => record.sessionId === sid) ?? null
}

async function save(input: { sessionId: string; intervalSeconds: number; prompt: string; paused: boolean }) {
  const saved = await forebrainApi.saveHeartbeat(input)
  generation += 1
  const index = records.value.findIndex((record) => record.sessionId === saved.sessionId)
  if (index >= 0) {
    // The write answers with the setting alone; the conversation's title is
    // the listing's, and stays.
    const next = [...records.value]
    next[index] = { ...saved, sessionTitle: records.value[index]?.sessionTitle }
    records.value = next
  } else {
    // Newest set up first, the order the gateway lists them in.
    records.value = [saved, ...records.value]
  }
  return saved
}

async function clear(sessionId: string) {
  await forebrainApi.clearHeartbeat(sessionId)
  generation += 1
  records.value = records.value.filter((record) => record.sessionId !== sessionId)
}

export function useHeartbeats() {
  return { records, refresh, forSession, save, clear, errorMessage: getErrorMessage }
}

// Heartbeats belong to the tenant's conversations: a switch forgets them.
onTenantReset(() => {
  generation += 1
  records.value = []
})
