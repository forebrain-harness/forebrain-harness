import { ref } from 'vue'

/**
 * The workbench is the collapsible third column of the chat page. It starts
 * collapsed — the owner's call: the default view is the rail and the
 * conversation, nothing else — and stays wherever the user put it within one
 * tab session. It is deliberately not persisted: every fresh load begins
 * collapsed again.
 */
const open = ref(false)

/**
 * The tab something asked to land on. A control that is *about* one tab — the
 * context ring is about the context, and nothing else — opens the panel on that
 * tab rather than on whichever tab was last read (prototype :4346).
 */
const requested = ref<string | null>(null)

export function useWorkbench() {
  return {
    open,
    requested,
    toggle() {
      open.value = !open.value
    },
    /** Open on a named tab, whatever the panel was showing before. */
    openOn(tab: string) {
      requested.value = tab
      open.value = true
    },
    /**
     * The panel has shown the tab it was asked for: forget the request, so that
     * clicking the same control again counts as a new one. A request is a
     * single-shot errand, not a standing preference — only the panel's own
     * "last tab read" is remembered.
     */
    consumed() {
      requested.value = null
    },
    close() {
      open.value = false
    },
  }
}
