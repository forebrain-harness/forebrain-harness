import { afterEach, describe, expect, it } from 'vitest'

import { usePanelWidth } from './usePanelWidth'

/** The handle as the panels wire it: one element carrying the drag handlers,
 *  which is what gives every event its `currentTarget`. */
function handleOf(panel: ReturnType<typeof usePanelWidth>) {
  const el = document.createElement('div')
  el.addEventListener('pointerdown', panel.onPointerDown)
  el.addEventListener('pointermove', panel.onPointerMove)
  el.addEventListener('pointerup', panel.onPointerUp)
  function drag(type: 'pointerdown' | 'pointermove' | 'pointerup', clientX: number) {
    el.dispatchEvent(new PointerEvent(type, { clientX, button: 0, pointerId: 1, bubbles: true }))
  }
  return { el, drag }
}

const BOUNDS = { min: 280, max: () => 720 }

describe('usePanelWidth', () => {
  afterEach(() => {
    document.body.className = ''
  })

  it('starts at the design width and keeps every write whole and inside the bounds', () => {
    const panel = usePanelWidth('panel-bounds', 340, BOUNDS)
    expect(panel.width.value).toBe(340)

    const { drag } = handleOf(panel)
    // 99.6px of travel left is a width in fractions; the panel is drawn at the
    // whole pixel it rounds to.
    drag('pointerdown', 400)
    drag('pointermove', 300.4)
    expect(panel.width.value).toBe(440)

    // Past the narrowest the panel may be narrowed to, a drag stops there…
    drag('pointermove', 900)
    expect(panel.width.value).toBe(280)
    // …and past the widest, the same.
    drag('pointermove', 0)
    expect(panel.width.value).toBe(720)
    drag('pointerup', 0)
  })

  it('widens by what the pointer travelled left, and marks the page while dragging', () => {
    const panel = usePanelWidth('panel-drag', 340, BOUNDS)
    const { drag } = handleOf(panel)
    expect(document.body.classList.contains('panel-resizing')).toBe(false)

    drag('pointerdown', 500)
    expect(document.body.classList.contains('panel-resizing')).toBe(true)
    drag('pointermove', 420)
    expect(panel.width.value).toBe(420)
    drag('pointerup', 420)
    expect(document.body.classList.contains('panel-resizing')).toBe(false)

    // A move with no drag in progress is the pointer passing over the edge, not
    // a drag: the panel keeps the width it has.
    drag('pointermove', 100)
    expect(panel.width.value).toBe(420)

    // Dragged further left than the window can spare: the panel stops at the
    // cap rather than taking the conversation's room.
    drag('pointerdown', 500)
    drag('pointermove', 100)
    expect(panel.width.value).toBe(720)
    drag('pointerup', 100)
    expect(document.body.classList.contains('panel-resizing')).toBe(false)
  })

  it('returns to the design width on reset and keeps one width per panel id', () => {
    const panel = usePanelWidth('panel-shared', 440, { min: 320, max: () => 860 })
    const { drag } = handleOf(panel)
    drag('pointerdown', 300)
    drag('pointermove', 200)
    drag('pointerup', 200)
    expect(panel.width.value).toBe(540)

    // The panel opened again in this tab is as wide as it was left; a reload is
    // what puts it back to the design's width, not closing it.
    expect(usePanelWidth('panel-shared', 440, { min: 320, max: () => 860 }).width.value).toBe(540)

    panel.reset()
    expect(panel.width.value).toBe(440)
    expect(usePanelWidth('panel-shared', 440, { min: 320, max: () => 860 }).width.value).toBe(440)

    // The keyboard form of the same gestures: a press of an arrow moves the
    // edge by 16px, Home is the double-click.
    panel.onKeydown(new KeyboardEvent('keydown', { key: 'ArrowLeft' }))
    expect(panel.width.value).toBe(456)
    panel.onKeydown(new KeyboardEvent('keydown', { key: 'ArrowRight' }))
    expect(panel.width.value).toBe(440)
    panel.onKeydown(new KeyboardEvent('keydown', { key: 'ArrowLeft' }))
    expect(panel.width.value).toBe(456)
    panel.onKeydown(new KeyboardEvent('keydown', { key: 'Home' }))
    expect(panel.width.value).toBe(440)

    // Another panel is untouched: the widths are kept by id, not as one.
    expect(usePanelWidth('panel-other', 340, BOUNDS).width.value).toBe(340)
  })
})
