import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import { forebrainApi, GatewayHttpError } from '@/lib/api'
import ReadonlyCode from '@/components/editor/ReadonlyCode.vue'
import FileDoc from './FileDoc.vue'

// jsdom cannot lay CodeMirror out, so the source view mounts a recorder (the
// same stand-in FileEditor.test and ReadonlyCode.test use). What this suite
// proves is FileDoc's own logic: the one bounded read, the status-worded
// refusals, the clamp and its notice, and that every whitelisted extension —
// md and html included — reaches the read-only source view, never a preview.
const state = vi.hoisted(() => ({
  cm: [] as Array<{ config: { doc: string } }>,
}))

vi.mock('codemirror', () => {
  class EditorView {
    static editable = { of: (value: unknown) => value }
    static lineWrapping = {}
    static scrollIntoView = (pos: number, opts: unknown) => ({ scroll: { pos, opts } })
    static decorations = { from: (field: unknown) => ({ decorationsFrom: field }) }
    state = { doc: { lines: 1, line: () => ({ from: 0 }) } }
    constructor(public config: { doc: string; extensions: unknown[] }) {
      state.cm.push(this)
    }
    dispatch() {}
    destroy() {}
  }
  return { EditorView, basicSetup: {} }
})

vi.mock('@codemirror/state', () => {
  class RangeSetBuilder {
    items: unknown[] = []
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
      return { of: (value: unknown) => ({ tag, value }), is: (e: { tag?: unknown }) => e?.tag === tag }
    },
  }
  const StateField = { define: (spec: unknown) => ({ field: spec }) }
  return { RangeSetBuilder, StateEffect, StateField }
})

vi.mock('@codemirror/view', () => ({
  Decoration: { none: { ranges: [] }, line: (spec: { class: string }) => ({ line: spec }) },
}))

function targetOf(overrides: Partial<{ path: string; lineFrom: number; lineTo: number }> = {}) {
  return {
    path: 'pkg/a.go',
    lineFrom: 2,
    lineTo: 3,
    scope: { sessionId: 's1' },
    ...overrides,
  }
}

describe('FileDoc', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => {
    setLocale('zh')
    vi.restoreAllMocks()
    state.cm.length = 0
  })

  it('reads the file through the conversation scope and hands the range to the source view', async () => {
    const read = vi.spyOn(forebrainApi, 'workspaceFile').mockResolvedValue({ path: 'pkg/a.go', content: 'a\nb\nc\nd' })
    const wrapper = mount(FileDoc, { props: { file: targetOf() } })
    expect(wrapper.get('[data-testid="file-doc-loading"]').text()).toBe('正在读取文件…')
    await flushPromises()
    expect(read).toHaveBeenCalledWith({ sessionId: 's1' }, 'pkg/a.go')
    const code = wrapper.findComponent(ReadonlyCode)
    expect(code.props('path')).toBe('pkg/a.go')
    expect(code.props('text')).toBe('a\nb\nc\nd')
    expect(code.props('highlightFrom')).toBe(2)
    expect(code.props('highlightTo')).toBe(3)
    expect(wrapper.find('[data-testid="file-doc-range-note"]').exists()).toBe(false)
  })

  it('opens md and html as source text, with no preview or browser route', async () => {
    for (const path of ['notes.md', 'page.html', 'view.htm']) {
      vi.spyOn(forebrainApi, 'workspaceFile').mockResolvedValue({ path, content: '# 标题\n<body>' })
      const wrapper = mount(FileDoc, { props: { file: targetOf({ path }) } })
      await flushPromises()
      expect(wrapper.findComponent(ReadonlyCode).exists()).toBe(true)
      expect(wrapper.find('iframe').exists()).toBe(false)
      expect(wrapper.find('img').exists()).toBe(false)
      vi.restoreAllMocks()
    }
  })

  it('clamps a range past the file end and says so', async () => {
    vi.spyOn(forebrainApi, 'workspaceFile').mockResolvedValue({ path: 'pkg/a.go', content: 'a\nb\nc' })
    const wrapper = mount(FileDoc, { props: { file: targetOf({ lineFrom: 10, lineTo: 20 }) } })
    await flushPromises()
    expect(wrapper.get('[data-testid="file-doc-range-note"]').text()).toBe('请求的行 10–20 超出文件共 3 行，已定位到文件末尾。')
    const code = wrapper.findComponent(ReadonlyCode)
    expect(code.props('highlightFrom')).toBe(3)
    expect(code.props('highlightTo')).toBe(3)
  })

  it('words each refusal by its status', async () => {
    const cases: Array<[number, string]> = [
      [404, '找不到文件 pkg/a.go。'],
      [413, 'pkg/a.go 超过了 1 MiB 的读取上限。'],
      [400, '这个路径打不开：pkg/a.go。'],
    ]
    for (const [status, text] of cases) {
      vi.spyOn(forebrainApi, 'workspaceFile').mockRejectedValue(new GatewayHttpError(status, 'gateway said no'))
      const wrapper = mount(FileDoc, { props: { file: targetOf() } })
      await flushPromises()
      expect(wrapper.get('[data-testid="file-doc-error"]').text()).toContain(text)
      expect(wrapper.findComponent(ReadonlyCode).exists()).toBe(false)
      vi.restoreAllMocks()
    }
  })

  it('any other failure keeps the message', async () => {
    vi.spyOn(forebrainApi, 'workspaceFile').mockRejectedValue(new Error('boom'))
    const wrapper = mount(FileDoc, { props: { file: targetOf() } })
    await flushPromises()
    expect(wrapper.get('[data-testid="file-doc-error"]').text()).toContain('读取文件失败：boom')
  })
})