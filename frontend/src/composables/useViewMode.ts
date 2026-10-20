/**
 * How much of a run the conversation draws: every card it produced (default),
 * or only the answers (concise).
 *
 * It is a reading mode, not a filter on the run — nothing is deleted, and the
 * gates waiting for the person's decision stay where they are. Each view keeps
 * its own choice: the conversation reads the mode under its session, a subtab
 * under the session and the agent it shows, so opening one subagent never
 * changes how another one — or the conversation above it — reads.
 */
import { computed, ref, watch, type WritableComputedRef } from 'vue'

export type ViewMode = 'default' | 'concise'

const MODES: ViewMode[] = ['default', 'concise']

/**
 * viewModeStorageKey names where a view's choice lives: the conversation's
 * session, or one subagent's tab when an agent id names it. Empty without a
 * session — a choice made before the conversation exists belongs nowhere.
 */
export function viewModeStorageKey(sessionId: string, agentId?: string): string {
  const sid = String(sessionId ?? '').trim()
  if (!sid) return ''
  const aid = String(agentId ?? '').trim()
  return `forebrain.viewMode.${sid}${aid ? `.${aid}` : ''}`
}

/**
 * readViewMode is the mode that view was left in, defaulting to the full
 * drawing. A storage that refuses to answer (private browsing throws on read)
 * reads as the default rather than as an error the reader has to clear.
 */
export function readViewMode(sessionId: string, agentId?: string): ViewMode {
  const key = viewModeStorageKey(sessionId, agentId)
  if (!key || typeof localStorage === 'undefined') return 'default'
  try {
    const stored = localStorage.getItem(key)
    return MODES.includes(stored as ViewMode) ? (stored as ViewMode) : 'default'
  } catch {
    return 'default'
  }
}

/**
 * writeViewMode remembers the choice for that view. A storage that refuses the
 * write (private browsing throws on set as well) leaves the session reading
 * the way it already was: the mode still works, it just does not outlive the
 * page.
 */
export function writeViewMode(mode: ViewMode, sessionId: string, agentId?: string): void {
  const key = viewModeStorageKey(sessionId, agentId)
  if (!key || typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(key, mode)
  } catch {
    // Nothing to restore later; the current view keeps what was chosen.
  }
}

/**
 * useViewMode owns one view's mode. The keys are read when the view is built
 * and re-read when the session or the agent it shows changes, so switching
 * conversations lands on the mode each of them was left in. What it returns is
 * a ref a chip can v-model: writing it remembers the choice for that view.
 */
export function useViewMode(sessionId: () => string, agentId?: () => string | undefined): {
  viewMode: WritableComputedRef<ViewMode>
  setViewMode: (mode: ViewMode) => void
} {
  const current = ref<ViewMode>(readViewMode(sessionId(), agentId?.()))

  watch(
    () => viewModeStorageKey(sessionId(), agentId?.()),
    () => {
      current.value = readViewMode(sessionId(), agentId?.())
    },
  )

  function setViewMode(mode: ViewMode): void {
    current.value = mode
    writeViewMode(mode, sessionId(), agentId?.())
  }

  const viewMode = computed<ViewMode>({
    get: () => current.value,
    set: (mode) => setViewMode(mode),
  })

  return { viewMode, setViewMode }
}
