import { inject, provide, type InjectionKey } from 'vue'
import { useRoute, useRouter } from 'vue-router'

/**
 * Where a card's "open the page that owns this" goes.
 *
 * The web's conversation page answers with vue-router: another conversation
 * is a query on `/`, a settings page is `/settings?tab=…`. The VS Code panel
 * runs the same cards with no router instance at all, so its shell publishes
 * its own two answers here and the cards inject them. One seam, one meaning
 * per errand — a card never learns which surface it is drawn on.
 */
export interface SurfaceNavigation {
  /** Another conversation, by id. */
  openConversation: (sessionId: string) => void
  /** A settings page: its tab, and the query that page reads. */
  openSettingsPage: (tab: string, query?: Record<string, string>) => void
}

/** Exported for the harnesses that mount one card under a surface's answers. */
export const SurfaceNavigationKey: InjectionKey<SurfaceNavigation> = Symbol('forebrain-surface-navigation')

/** Publishes this surface's answers to its subtree. */
export function provideSurfaceNavigation(navigation: SurfaceNavigation): SurfaceNavigation {
  provide(SurfaceNavigationKey, navigation)
  return navigation
}

/**
 * The navigation a card calls. Where a surface published its own, that is the
 * answer; otherwise the router's — the web page's own two pushes, unchanged.
 * The router is only asked for when it is the answer, so a surface without one
 * never injects a key nothing provides.
 */
export function useSurfaceNavigation(): SurfaceNavigation {
  const provided = inject(SurfaceNavigationKey, null)
  if (provided) return provided
  const router = useRouter()
  const route = useRoute()
  return {
    openConversation(sessionId: string) {
      void router.push({ path: '/', query: { ...route.query, session: sessionId } })
    },
    openSettingsPage(tab: string, query?: Record<string, string>) {
      void router.push({ path: '/settings', query: { tab, ...query } })
    },
  }
}