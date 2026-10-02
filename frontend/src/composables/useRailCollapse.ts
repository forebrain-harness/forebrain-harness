import { ref } from 'vue'

/**
 * The rail starts expanded (the owner's default) and remembers its state for
 * the tab session only — a reload is a fresh start, expanded.
 */
const collapsed = ref(false)

export function useRailCollapse() {
  return {
    collapsed,
    toggle() {
      collapsed.value = !collapsed.value
    },
    collapse() {
      collapsed.value = true
    },
    expand() {
      collapsed.value = false
    },
  }
}
