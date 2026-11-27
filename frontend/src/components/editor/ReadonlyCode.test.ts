import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ReadonlyCode from './ReadonlyCode.vue'

// jsdom cannot lay CodeMirror out, so the editor is a recorder (the same
// stand-in FileEditor.test uses): this exercises the component's own logic —
// the range it paints, the clamp, the centre scroll, the fade timer and its
// cleanup. The rendered editor is covered by the e2e suite.
const state = vi.hoisted(() => ({
  cm: [] as Array<{
    config: { doc: string; extensions: unknown[] }
    dispatches: Array<{ effects?: Array<Record<string, unknown>> }>
    destroyed: boolean
  }>,
}))

vi.mock('codemirror', () => {
  class EditorView {
    static editable = { of: (value: unknown) => value }
    static lineWrapping = {}
    static scrollIntoView = (pos: number, opts: unknown) => ({ scroll: { pos, opts } })
    static decorations = { from: (field: unknown) => ({ decorationsFrom: field }) }
    config: { doc: string; extensions: unknown[] }
    dispatches: Array<{ effects?: Array<Record<string, unknown>> }> = []
    destroyed = false
    state: { doc: { lines: number; line: (n: number) => { from: number } } }
    constructor(config: { doc: string; extensions: unknown[] }) {
      this.config = config
      const lines = config.doc.split('\n')
      const offsets: number[] = []
      let at = 0
      for (const line of lines) {
        offsets.push(at)
        at += line.length + 1
      }
      this.state = { doc: { lines: lines.length, line: (n) => ({ from: offsets[n - 1] ?? 0 }) } }
      state.cm.push(this)
    }
    dispatch(tr: { effects?: Array<Record<string, unknown>> }) {
      this.dispatches.push(tr)
    }
    destroy() {
      this.destroyed = true
    }
  }
  return { EditorView, basicSetup: {} }
})

vi.mock('@codemirror/state', () => {
  class RangeSetBuilder {
    items: Array<{ from: number; to: number; deco: unknown }> = []
    add(from: number, to: number, deco: unknown) {
      this.items.push({ from, to, deco })
    }
    finish() {
      return { ranges: this.items }
    }
  }
  const StateEffect = {
    define: () => {
      const tag = {}
      return {
        of: (value: unknown) => ({ tag, value }),
        is: (effect: { tag?: unknown }) => effect?.tag === tag,
      }
    },
  }
  const StateField = { define: (spec: unknown) => ({ field: spec }) }
  return { RangeSetBuilder, StateEffect, StateField }
})

vi.mock('@codemirror/view', () => ({
  Decoration: {
    none: { ranges: [] },
    line: (spec: { class: string }) => ({ line: spec }),
  },
}))

beforeEach(() => {
  state.cm.length = 0
})

afterEach(() => {
  vi.useRealTimers()
})

/** The highlight effect's painted lines: their offsets and class. */
function painted(view: (typeof state.cm)[number], dispatchIndex: number) {
  const effect = view.dispatches[dispatchIndex]?.effects?.[0] as { value?: { ranges: Array<{ from: number; deco: { line: { class: string } } }> } }
  return effect?.value?.ranges ?? []
}

describe('ReadonlyCode highlight', () => {
  it('paints the requested range and centres its first line', async () => {
    mount(ReadonlyCode, {
      props: { path: 'a.go', text: 'l1\nl2\nl3\nl4\nl5', highlightFrom: 2, highlightTo: 4 },
    })
    await vi.waitFor(() => expect(state.cm).toHaveLength(1))
    const view = state.cm[0]
    expect(view.config.doc).toBe('l1\nl2\nl3\nl4\nl5')
    const ranges = painted(view, 0)
    expect(ranges.map((r) => r.from)).toEqual([3, 6, 9])
    expect(ranges.every((r) => r.deco.line.class === 'cm-file-line-highlight')).toBe(true)
    const scroll = view.dispatches[0].effects?.[1] as { scroll: { pos: number; opts: { y: string } } }
    expect(scroll.scroll).toEqual({ pos: 3, opts: { y: 'center' } })
  })

  it('clamps a range past the file end to the last line', async () => {
    mount(ReadonlyCode, {
      props: { path: 'a.go', text: 'l1\nl2\nl3', highlightFrom: 10, highlightTo: 20 },
    })
    await vi.waitFor(() => expect(state.cm).toHaveLength(1))
    const ranges = painted(state.cm[0], 0)
    expect(ranges.map((r) => r.from)).toEqual([6])
  })

  it('holds the mark, then dispatches it away so the line transition fades', async () => {
    vi.useFakeTimers()
    mount(ReadonlyCode, {
      props: { path: 'a.go', text: 'l1\nl2\nl3', highlightFrom: 1, highlightTo: 2 },
    })
    await flushPromises()
    expect(state.cm).toHaveLength(1)
    const view = state.cm[0]
    expect(painted(view, 0)).toHaveLength(2)
    vi.advanceTimersByTime(2499)
    expect(view.dispatches).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(view.dispatches).toHaveLength(2)
    expect(painted(view, 1)).toEqual([])
  })

  it('a new range repaints and recentres without remounting', async () => {
    const wrapper = mount(ReadonlyCode, {
      props: { path: 'a.go', text: 'l1\nl2\nl3\nl4', highlightFrom: 1, highlightTo: 1 },
    })
    await vi.waitFor(() => expect(state.cm).toHaveLength(1))
    await wrapper.setProps({ highlightFrom: 3, highlightTo: 4 })
    expect(state.cm).toHaveLength(1)
    const view = state.cm[0]
    expect(view.dispatches).toHaveLength(2)
    expect(painted(view, 1).map((r) => r.from)).toEqual([6, 9])
    const scroll = view.dispatches[1].effects?.[1] as { scroll: { pos: number } }
    expect(scroll.scroll.pos).toBe(6)
  })

  it('unmount clears the fade timer', async () => {
    vi.useFakeTimers()
    const wrapper = mount(ReadonlyCode, {
      props: { path: 'a.go', text: 'l1\nl2', highlightFrom: 1 },
    })
    await flushPromises()
    const view = state.cm[0]
    wrapper.unmount()
    expect(view.destroyed).toBe(true)
    vi.advanceTimersByTime(3000)
    expect(view.dispatches).toHaveLength(1)
  })

  it('without a range nothing is painted', async () => {
    mount(ReadonlyCode, { props: { path: 'a.go', text: 'l1\nl2' } })
    await vi.waitFor(() => expect(state.cm).toHaveLength(1))
    expect(painted(state.cm[0], 0)).toEqual([])
  })
})