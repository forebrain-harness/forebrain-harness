import { ref } from 'vue'

/**
 * The workbench is the collapsible third column of the chat page. It starts
 * collapsed — the owner's call: the default view is the rail and the
 * conversation, nothing else — and stays wherever the user put it within one
 * tab session. It is deliberately not persisted: every fresh load begins
 * collapsed again.
 */
const open = ref(false)

export function useWorkbench() {
  return {
    open,
    toggle() {
      open.value = !open.value
    },
    close() {
      open.value = false
    },
  }
}
