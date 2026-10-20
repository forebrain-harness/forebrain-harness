import { ref, watch, type Ref } from 'vue'

import { forebrainApi } from '@/lib/api'

/** One plan document the conversation wrote: the file it lives in, when that
 *  version was written, and its text. A conversation that replans writes a new
 *  file each time, so the list is how earlier versions stay reachable — the
 *  single "current plan" read can only ever name the newest. */
export type SessionPlan = {
  name: string
  /** Relative to the state root — what the drawer's meta strip shows. */
  path: string
  /** Unix seconds. */
  updatedAt: number
  markdown: string
}

/** A plan document open as a tab in the drawer the subagents use: the file it
 *  came from, the three facts its meta strip states, and its text. */
export type PlanTab = {
  /** `plan:<file name>` — never collides with a subagent id. */
  id: string
  label: string
  path: string
  at: string
  done: number
  total: number
  markdown: string
}

/**
 * The conversation's plan files, newest first — the order the history card
 * draws them in, and the order a reader needs to tell the current plan from
 * the versions it replaced. `refresh` is the caller's trigger for re-reading
 * them — the workbench passes the same signal that reloads the rest of its
 * cards.
 */
export function useSessionPlans(sessionId: Ref<string | null>, refresh?: () => unknown) {
  const plans = ref<SessionPlan[]>([])
  const loading = ref(false)
  let generation = 0

  async function reload() {
    const sid = String(sessionId.value ?? '').trim()
    const current = ++generation
    if (!sid) {
      plans.value = []
      loading.value = false
      return
    }
    loading.value = true
    try {
      const list = await forebrainApi.sessionPlans(sid)
      if (current === generation) {
        plans.value = (Array.isArray(list) ? list : []).sort((a, b) => b.updatedAt - a.updatedAt)
      }
    } catch {
      // A list that cannot be read leaves the previous one on screen: an
      // unnoticed refresh failure must not empty the history a reader is
      // looking at.
    } finally {
      if (current === generation) loading.value = false
    }
  }

  watch([sessionId, () => (refresh ? refresh() : null)], () => void reload(), { immediate: true })

  return { plans, loading, reload }
}
