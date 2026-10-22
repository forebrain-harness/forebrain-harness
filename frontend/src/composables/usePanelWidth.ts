import { ref } from 'vue'

/**
 * The right-hand panels' widths, one per panel id, for as long as this tab is
 * open: closing a panel and opening it again keeps the width it was dragged
 * to, and a reload is the design's own width again (owner 2026-10-21 — the
 * same rule as a table's column widths). Deliberately not persisted: nothing
 * here touches localStorage.
 */
const widths = new Map<string, number>()

export interface PanelBounds {
  /** The narrowest the panel may be dragged to. */
  min: number
  /** The widest — read at the moment of each write, because what the window
   *  can spare depends on its current width. */
  max: () => number
}

/**
 * One panel's width, and the four gestures that change it: dragging the handle
 * on the panel's left edge, double-clicking it, and the arrow keys and Home
 * while it holds the focus. The two panels of the conversation page share this
 * one implementation — their only differences are the id, the design width and
 * the bounds.
 */
export function usePanelWidth(id: string, initial: number, bounds: PanelBounds) {
  const width = ref(widths.get(id) ?? initial)
  // The drag in progress: where the pointer went down, the width it started
  // from, and the element that took the capture — absent means no drag.
  let startX = 0
  let startWidth = 0
  let capturing: Element | null = null

  /** Every write goes through here, so the stored width is the drawn one. */
  function apply(next: number): void {
    width.value = Math.round(Math.min(Math.max(next, bounds.min), bounds.max()))
    widths.set(id, width.value)
  }

  function stop(event: PointerEvent): void {
    capturing?.releasePointerCapture?.(event.pointerId)
    capturing = null
    if (typeof document !== 'undefined') document.body.classList.remove('panel-resizing')
  }

  function onPointerDown(event: PointerEvent): void {
    // The primary button only: a right-click on the edge is not a drag.
    if (event.button !== 0) return
    startX = event.clientX
    startWidth = width.value
    capturing = (event.currentTarget as Element | null) ?? null
    // The pointer leaves the 6px strip immediately — that is what the gesture
    // is for — so the moves have to keep arriving at the handle anyway.
    capturing?.setPointerCapture?.(event.pointerId)
    if (typeof document !== 'undefined') document.body.classList.add('panel-resizing')
    event.preventDefault()
  }

  /** The handle sits on the panel's left edge: moving left widens the panel. */
  function onPointerMove(event: PointerEvent): void {
    if (!capturing) return
    apply(startWidth + (startX - event.clientX))
  }

  /** Pointer up and pointer cancel end a drag the same way. */
  function onPointerUp(event: PointerEvent): void {
    if (!capturing) return
    stop(event)
  }

  const STEP = 16

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'ArrowLeft') apply(width.value + STEP)
    else if (event.key === 'ArrowRight') apply(width.value - STEP)
    else if (event.key === 'Home') apply(initial)
    else return
    event.preventDefault()
  }

  /** Double-click, or a page's own control: back to the design's width. */
  function reset(): void {
    apply(initial)
  }

  return { width, onPointerDown, onPointerMove, onPointerUp, onKeydown, reset }
}
