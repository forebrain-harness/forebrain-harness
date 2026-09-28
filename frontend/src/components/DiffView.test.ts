import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import DiffView from './DiffView.vue'

/**
 * Diff rows carry the same syntax highlighting as every other code surface, so
 * a file reads the same whether it is shown as a read result or as a change.
 * Shiki loads its grammar asynchronously, so the first frame is plain text and
 * the colours arrive after.
 */
describe('DiffView', () => {
  const files = [{
    path: 'sample.go',
    added: 2,
    deleted: 0,
    hunks: [{
      old_start: 1,
      new_start: 1,
      lines: [
        { kind: 'add' as const, new_no: 1, text: 'const limit = 42' },
        { kind: 'ctx' as const, old_no: 1, new_no: 2, text: 'func describe() string {' },
      ],
    }],
  }]

  it('paints code with the Monokai pair', { timeout: 30000 }, async () => {
    const wrapper = mount(DiffView, { props: { files, totalLines: 2 } })

    // Plain text first, and never anything but the code itself.
    expect(wrapper.text()).toContain('const limit = 42')

    await vi.waitFor(() => {
      const styles = wrapper.findAll('.diff-code span')
        .map(span => (span.attributes('style') ?? '').toLowerCase())
      // The same pair the terminal renderer resolves: Monokai's storage cyan
      // in the dark palette, its light sibling's in the light one. A rendered
      // style carries the light colour outright and the dark one as the custom
      // property the theme swap reads.
      expect(styles.some(style => style.includes('--shiki-dark: #66d9ef'))).toBe(true)
      expect(styles.some(style => style.includes('rgb(0, 168, 200)'))).toBe(true)
      // Monokai's green identifiers, likewise in both palettes.
      expect(styles.some(style => style.includes('--shiki-dark: #a6e22e'))).toBe(true)
      expect(styles.some(style => style.includes('rgb(117, 175, 0)'))).toBe(true)
    }, { timeout: 20000 })

    // Highlighting adds no text of its own: the row still copies clean.
    expect(wrapper.text()).toContain('func describe() string {')
  })

  it('keeps rows readable when the language is unknown', async () => {
    const wrapper = mount(DiffView, {
      props: {
        files: [{ ...files[0], path: 'notes.unknownext' }],
        totalLines: 2,
      },
    })
    expect(wrapper.text()).toContain('const limit = 42')
  })
})
