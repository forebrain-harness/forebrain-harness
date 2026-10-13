import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import FileEditor from './FileEditor.vue'

// jsdom cannot run Crepe or CodeMirror for real (no layout), so both are
// replaced with recorders: this exercises the component's own logic — which
// editor it opens with, that the text survives a WYSIWYG/source round trip, and
// when it reports itself dirty. The rendered editors are covered by the e2e
// suite.
const state = vi.hoisted(() => ({
  crepe: [] as Array<{ options: { defaultValue?: unknown } }>,
  cm: [] as Array<{ config: { doc: string; extensions: unknown[] } }>,
}))

vi.mock('@milkdown/crepe', () => {
  const Feature = {
    CodeMirror: 'code-mirror',
    ListItem: 'list-item',
    LinkTooltip: 'link-tooltip',
    Cursor: 'cursor',
    ImageBlock: 'image-block',
    BlockEdit: 'block-edit',
    Toolbar: 'toolbar',
    Placeholder: 'placeholder',
    Table: 'table',
    Latex: 'latex',
    TopBar: 'top-bar',
    AI: 'ai',
  }
  class Crepe {
    static Feature = Feature
    options: { defaultValue?: unknown }
    constructor(options: { defaultValue?: unknown }) {
      this.options = options
      state.crepe.push(this)
    }
    on() {
      return this
    }
    async create() {
      return this
    }
    async destroy() {
      return this
    }
    setReadonly() {
      return this
    }
    // A faithful stand-in: the real editor hands back what it holds, so the
    // component's own post-create re-baselining is exercised.
    getMarkdown() {
      return typeof this.options.defaultValue === 'string' ? this.options.defaultValue : ''
    }
  }
  return { Crepe, CrepeFeature: Feature }
})

vi.mock('codemirror', () => {
  class EditorView {
    static updateListener = { of: (fn: unknown) => fn }
    static editable = { of: (value: unknown) => value }
    static lineWrapping = {}
    constructor(config: { doc: string; extensions: unknown[] }) {
      state.cm.push({ config })
    }
    destroy() {}
  }
  return { EditorView, basicSetup: {} }
})

beforeEach(() => {
  state.crepe.length = 0
  state.cm.length = 0
})

function lastStyleInput(wrapper: ReturnType<typeof mount>): unknown[] {
  const emitted = wrapper.emitted('update:modelValue') ?? []
  return emitted[emitted.length - 1] ?? []
}

describe('FileEditor', () => {
  it('opens Markdown in WYSIWYG mode with a mode toggle', async () => {
    const wrapper = mount(FileEditor, { props: { path: 'AGENTS.md', modelValue: '# 准则\n' } })
    await flushPromises()
    expect(state.crepe).toHaveLength(1)
    expect(wrapper.get('[data-testid="editor-mode-rich"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-testid="editor-mode-src"]').attributes('aria-pressed')).toBe('false')
    expect(wrapper.text()).toContain('Markdown')
  })

  it('opens a code file in CodeMirror with no mode toggle', async () => {
    const wrapper = mount(FileEditor, { props: { path: 'scripts/check-health.sh', modelValue: 'echo hi\n' } })
    // The language grammar loads on demand, so the view appears a tick later.
    await vi.waitFor(() => expect(state.cm).toHaveLength(1))
    expect(state.crepe).toHaveLength(0)
    expect(state.cm[0].config.doc).toBe('echo hi\n')
    expect(wrapper.find('[data-testid="editor-mode-src"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Bash')
  })

  it('carries the text across a source round trip and reports dirty', async () => {
    const wrapper = mount(FileEditor, { props: { path: 'AGENTS.md', modelValue: '# 准则\n' } })
    await flushPromises()
    expect(wrapper.emitted('dirty')?.[0]).toEqual([false])

    await wrapper.get('[data-testid="editor-mode-src"]').trigger('click')
    await flushPromises()
    const view = state.cm[state.cm.length - 1]
    expect(view.config.doc).toBe('# 准则\n')
    // Switching mode alone is not an edit.
    const dirty = wrapper.emitted('dirty') ?? []
    expect(dirty[dirty.length - 1]).toEqual([false])

    const listener = view.config.extensions.find((extension) => typeof extension === 'function') as (update: {
      docChanged: boolean
      state: { doc: { toString: () => string } }
    }) => void
    listener({ docChanged: true, state: { doc: { toString: () => '# 准则（改）\n' } } })
    await flushPromises()
    expect(lastStyleInput(wrapper)).toEqual(['# 准则（改）\n'])
    const dirtyAfter = wrapper.emitted('dirty') ?? []
    expect(dirtyAfter[dirtyAfter.length - 1]).toEqual([true])

    // Back to WYSIWYG: the edited text is what the Crepe body is built from.
    await wrapper.get('[data-testid="editor-mode-rich"]').trigger('click')
    await flushPromises()
    expect(state.crepe[state.crepe.length - 1].options.defaultValue).toBe('# 准则（改）\n')
  })

  it('splits YAML front matter off the WYSIWYG body and shows it as fields', async () => {
    const source = '---\nname: gateway-ops\ndescription: 网关排障\n---\n\n# gateway-ops\n'
    const wrapper = mount(FileEditor, { props: { path: 'SKILL.md', modelValue: source } })
    await flushPromises()
    expect(state.crepe[state.crepe.length - 1].options.defaultValue).toBe('\n# gateway-ops\n')
    const inputs = wrapper.findAll('.mde-props input')
    expect(inputs.map((input) => (input.element as HTMLInputElement).value)).toEqual(['gateway-ops', '网关排障'])
  })
})
