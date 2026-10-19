import { ref } from 'vue'
import { getErrorMessage, forebrainApi, type ChatSessionRecord } from '@/lib/api'
import { t } from '@/locales'

export interface ChatSessionItem {
  id: string
  title: string | null
  createTime: string
  updateTime: string
  /** What the session is for: '' ordinary, 'workshop' a skill-workshop task. */
  source: string
  /** The project the conversation belongs to, '' for a project-less
   * workshop task. */
  projectName: string
}

/** One drawer page: the list contract's page-size ceiling. */
const drawerPageSize = 100

/**
 * The primary agent's conversation list, as one shared fact: the rail's
 * drawer lists it, a finished run refreshes it, and the chat page watches
 * it as the signal to re-read the open conversation. Module-level state,
 * like every tenant-scoped store here.
 *
 * The drawer pages through the list by appending ("load more") rather than
 * the page replacement the table composable does, so the pager lives here:
 * fetchSessions reloads the first page — a refresh means going back to the
 * newest rows — loadMore appends the next page deduped by id, and hasMore
 * is what the server answered, never a count the live list could drift
 * away from (a running conversation bumps its updated_at and reorders
 * pages under the drawer).
 */
const sessions = ref<ChatSessionItem[]>([])
const loading = ref(false)
const loadingMore = ref(false)
const error = ref<string | null>(null)
const total = ref(0)
/** Pages fully loaded so far; the load-more asks for the next one. */
const page = ref(1)
const hasMore = ref(false)
/** The drawer's search box, as the server last saw it. */
const search = ref('')

// The newest request's sequence number: an answer whose number is stale is
// dropped instead of applied, the same guard every paged list carries.
let latest = 0

function toItems(records: ChatSessionRecord[]): ChatSessionItem[] {
  return records.map((r) => ({
    id: r.id,
    title: r.title || t('chatDrawer.untitled'),
    createTime: r.createTime,
    updateTime: r.updateTime,
    source: r.source ?? '',
    projectName: r.projectName ?? '',
  }))
}

async function fetchSessions(q?: string) {
  if (q !== undefined) search.value = q
  const mine = ++latest
  loading.value = true
  error.value = null
  try {
    const data = await forebrainApi.chatSessions('', { page: 1, pageSize: drawerPageSize, q: search.value })
    if (mine !== latest) return
    sessions.value = toItems(data?.records ?? [])
    total.value = data?.total ?? sessions.value.length
    page.value = 1
    hasMore.value = sessions.value.length < total.value
  } catch (e) {
    if (mine !== latest) return
    sessions.value = []
    total.value = 0
    page.value = 1
    hasMore.value = false
    error.value = getErrorMessage(e)
  } finally {
    if (mine === latest) loading.value = false
  }
}

async function loadMore() {
  if (loading.value || loadingMore.value || !hasMore.value) return
  const mine = ++latest
  loadingMore.value = true
  error.value = null
  try {
    const data = await forebrainApi.chatSessions('', {
      page: page.value + 1,
      pageSize: drawerPageSize,
      q: search.value,
    })
    if (mine !== latest) return
    const records = toItems(data?.records ?? [])
    // Dedupe by id: pages reorder live as conversations bump their
    // updated_at, so the next page can overlap the rows already listed.
    const known = new Set(sessions.value.map((row) => row.id))
    const fresh = records.filter((row) => !known.has(row.id))
    if (data?.total !== undefined) total.value = data.total
    sessions.value = [...sessions.value, ...fresh]
    // The server says when the list ends, not the row count: a page that
    // came back short, or one whose rows were all already listed (a
    // past-the-end request serves the last page), leaves nothing more to
    // ask for. Otherwise the page count advances with what it added.
    if (records.length < drawerPageSize || fresh.length === 0) {
      hasMore.value = false
    } else {
      page.value += 1
      hasMore.value = sessions.value.length < total.value
    }
  } catch (e) {
    // A failed page keeps what is loaded; the person can try again.
    if (mine !== latest) return
    error.value = getErrorMessage(e)
  } finally {
    if (mine === latest) loadingMore.value = false
  }
}

/**
 * Open a new conversation: create it, refresh the list, and hand back the
 * row. A failure is the caller's to show — nothing here resets silently. Since
 * plan 029 an ordinary conversation is opened into a project, so the caller
 * names one (`projectId`) or a folder the gateway turns into a project.
 */
async function createSession(opts: { title?: string; source?: '' | 'workshop'; projectId?: string; folder?: string } = {}) {
  const created = await forebrainApi.chatSessionCreate(opts)
  await fetchSessions()
  return created
}

export function useChatSessions() {
  return { sessions, loading, loadingMore, error, total, page, hasMore, search, fetchSessions, loadMore, createSession }
}

import { onTenantReset } from '@/composables/useTenantScope'

// Sessions belong to the tenant: a switch empties the list until the next
// fetch, so no previous agent's conversation can appear under the new one.
onTenantReset(() => {
  sessions.value = []
  loading.value = false
  loadingMore.value = false
  error.value = null
  total.value = 0
  page.value = 1
  hasMore.value = false
  search.value = ''
})
