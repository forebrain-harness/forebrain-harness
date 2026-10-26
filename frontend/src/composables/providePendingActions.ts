import { inject, provide, type InjectionKey } from 'vue'
import type { usePendingActions } from './usePendingActions'

/**
 * The conversation's one pending-actions store, shared down its tree.
 *
 * The waiting card hangs on the tool call it holds (plan 138), so the cards
 * deep in the timeline need the same loaded requests, answers and error state
 * the conversation's tail surface reads. The page calls `usePendingActions`
 * once and hands it here; every card injects it. A card rendered outside a
 * provider — a test harness, a story — injects null and simply carries no
 * pending request, the state it had before this existed.
 */
export type PendingActions = ReturnType<typeof usePendingActions>

/** Exported for the test harnesses that mount one card under a store. */
export const PendingActionsKey: InjectionKey<PendingActions> = Symbol('forebrain-pending-actions')

/** Publishes the conversation's one pending-actions store to its subtree. */
export function providePendingActions(store: PendingActions): PendingActions {
  provide(PendingActionsKey, store)
  return store
}

/** The subtree's pending-actions store, or null where none was provided. */
export function injectPendingActions(): PendingActions | null {
  return inject(PendingActionsKey, null)
}
