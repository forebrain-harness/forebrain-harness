import { onBeforeUnmount, ref, watch, type Ref } from 'vue'

/**
 * useTruncated reports whether an element's own line is wider than the room
 * it got — the card head's title area, whose object ends in an ellipsis when
 * it does not fit. The eye button that opens the whole text in the drawer
 * appears exactly then (owner 2026-10-25).
 *
 * The element itself and its direct children are measured: the title area is
 * a flex row whose children shrink instead of overflowing, so the cut shows
 * in a child's scrollWidth, not the row's.
 */
export function useTruncated(el: Ref<HTMLElement | null>): { truncated: Ref<boolean>; recheck: () => void } {
  const truncated = ref(false)
  let observer: ResizeObserver | null = null

  function measureOne(node: HTMLElement): boolean {
    return node.scrollWidth > node.clientWidth + 1
  }

  function check() {
    const root = el.value
    if (!root) {
      truncated.value = false
      return
    }
    truncated.value = measureOne(root) || Array.from(root.children).some(
      (child) => child instanceof HTMLElement && measureOne(child),
    )
  }

  /** Re-measures for the moments the content changed without the box resizing. */
  function recheck() {
    check()
  }

  function disconnect() {
    observer?.disconnect()
    observer = null
  }

  watch(el, (node) => {
    disconnect()
    if (!node || typeof ResizeObserver === 'undefined') return
    observer = new ResizeObserver(() => check())
    observer.observe(node)
    check()
  }, { immediate: true, flush: 'post' })

  onBeforeUnmount(disconnect)

  return { truncated, recheck }
}
