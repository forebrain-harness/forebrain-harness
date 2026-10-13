import { onUnmounted, shallowRef, type VNode } from 'vue'

/**
 * What one view puts in the shell's global top bar. The factories are called
 * inside the shell's render, so whatever reactive state they read keeps the
 * bar current without the view re-registering.
 */
export interface TopbarContent {
  /** Drawn in place of the shell's own page-title span. */
  leading?: () => VNode[]
  /** Drawn after the spacer, before the shell's own "new agent" and language
   *  controls — the page's own buttons stay right of everything it adds. */
  trailing?: () => VNode[]
}

const content = shallowRef<TopbarContent | null>(null)

// The latest writer owns the bar: a view leaving late must not clear the row
// of the view that already mounted.
let owner = 0

/**
 * The smallest shared mechanism the shell has for page-contributed top-bar
 * content: a view fills its row during setup and the slot is vacated when it
 * unmounts. Nothing else about the shell changes — the title it draws today
 * stays for every page that fills nothing.
 */
export function useTopbar() {
  const id = ++owner
  const set = (next: TopbarContent | null) => {
    owner = id
    content.value = next
  }
  onUnmounted(() => {
    if (owner === id) content.value = null
  })
  return { set }
}

/** What the shell renders; the one source of the contributed row. */
export function useTopbarContent() {
  return content
}
