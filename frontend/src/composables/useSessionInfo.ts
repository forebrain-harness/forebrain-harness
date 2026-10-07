import { ref } from 'vue'
import { forebrainApi, type ChatSessionInfo } from '@/lib/api'
import { onTenantReset } from '@/composables/useTenantScope'

/**
 * The open conversation's own facts — its title and the project it belongs
 * to — read from the gateway by id. The chat page names its header from it
 * instead of looking the session up in the drawer's list, which lists only
 * the agent's own conversations: a project's session is never in it.
 * Module-level state, like every tenant-scoped store here.
 */
const sessionInfo = ref<ChatSessionInfo | null>(null)
let sessionInfoRequest = 0

/**
 * The header's name for the open conversation: the session's own title, or
 * the surface's placeholder while it has none yet.
 */
export function sessionHeaderTitle(info: { title: string } | null | undefined, placeholder: string): string {
  return info?.title || placeholder
}

async function loadSessionInfo(sid: string | null) {
  const request = ++sessionInfoRequest
  sessionInfo.value = null
  if (!sid) return
  try {
    const res = await forebrainApi.chatSession(sid)
    if (request === sessionInfoRequest) sessionInfo.value = res
  } catch {
    // The tag is a pointer, not content: without it the conversation reads
    // exactly as it does, and the page's own loads report the failure.
  }
}

export function useSessionInfo() {
  return { sessionInfo, loadSessionInfo }
}

// The facts belong to the tenant: a switch drops them until the open
// session is read again, so no previous agent's title can name a page.
onTenantReset(() => {
  sessionInfo.value = null
})
