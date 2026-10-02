import { readonly, ref } from 'vue'

/**
 * A primary agent switch redraws every tenant boundary the frontend holds:
 * sessions, projects, cron, channels, providers, tools, permission rules,
 * memories, the roster — and the workspace tree (which resets itself on the
 * agents store's refreshToken). Pages register a reset callback here and
 * trust it to run exactly once per switch.
 */
const callbacks = new Set<() => void>()
const resetCount = ref(0)
const activeProjectId = ref('')

/** The project space currently open, when there is one. */
export function setActiveProject(id: string): void {
  if (activeProjectId.value === id) return
  activeProjectId.value = id
  if (!id) return
  // Entering a different project redraws the project boundary the same way
  // a tenant switch redraws the tenant one.
  resetCount.value += 1
  for (const callback of [...callbacks]) {
    try {
      callback()
    } catch {
      // See resetTenantScope.
    }
  }
}

export function resetTenantScope(): void {
  activeProjectId.value = ''

  resetCount.value += 1
  for (const callback of [...callbacks]) {
    try {
      callback()
    } catch {
      // A failing reset must not block the others; the page it belongs to
      // reports its own load errors.
    }
  }
}

export function onTenantReset(callback: () => void): () => void {
  callbacks.add(callback)
  return () => callbacks.delete(callback)
}

export function useTenantScope() {
  return { resetCount: readonly(resetCount), activeProjectId: readonly(activeProjectId) }
}
