import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import DiffView from './DiffView.vue'

/**
 * The turn diff is drawn in the one unified style every diff shares (plan
 * 137): the changed-file row, the shared DiffBlock rows beneath it, and the
 * per-file fold with its small-diff auto-expand.
 */
describe('DiffView', () => {
  const files = [{
    path: 'sample.go',
    status: 'modified',
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

  it('draws the shared DiffBlock rows under the changed-file row', async () => {
    const wrapper = mount(DiffView, { props: { files, totalLines: 2 } })
    // Small diffs open by themselves: the rows are there without a click.
    const rows = wrapper.findAll('.dr')
    expect(rows.map((row) => row.classes())).toEqual([['dr', 'add'], ['dr']])
    expect(rows[0]!.find('.g').text()).toBe('1')
    expect(rows[0]!.find('.t').text()).toContain('const limit = 42')
    expect(rows[1]!.find('.t').text()).toContain('func describe() string {')
    // The head row reads the way the shell card’s changed files do.
    expect(wrapper.get('.fc-fn').text()).toBe('sample.go')
    expect(wrapper.get('.fc-n').text()).toBe('+2')
  })

  it('folds per file, and a small total opens every file', async () => {
    const two = [...files, { ...files[0], path: 'other.go' }]
    const wrapper = mount(DiffView, { props: { files: two, totalLines: 2 } })
    expect(wrapper.findAll('[data-testid^="diff-file-"]')).toHaveLength(2)
    expect(wrapper.findAll('.dr')).toHaveLength(4)

    // A big total starts folded; the fold opens one file at a time.
    const folded = mount(DiffView, { props: { files: two, totalLines: 400 } })
    expect(folded.findAll('.dr')).toHaveLength(0)
    await folded.get('[data-testid="diff-file-0"]').trigger('click')
    expect(folded.findAll('.dr')).toHaveLength(2)
    expect(folded.get('[data-testid="diff-file-0"]').attributes('aria-expanded')).toBe('true')
  })

  it('draws an added file as plain code and keeps the fallback rows honest', () => {
    const added = mount(DiffView, {
      props: {
        files: [{ path: 'new.go', status: 'added', added: 2, deleted: 0, hunks: files[0]!.hunks }],
        totalLines: 2,
      },
    })
    expect(added.findAll('.dr.add')).toHaveLength(0)
    expect(added.text()).toContain('const limit = 42')

    const binary = mount(DiffView, {
      props: { files: [{ path: 'logo.png', status: 'modified', binary: true }], totalLines: 0 },
    })
    expect(binary.text()).toContain('Binary file changed.')
    const empty = mount(DiffView, {
      props: { files: [{ path: 'mode.sh', status: 'mode-only' }], totalLines: 0 },
    })
    expect(empty.text()).toContain('mode only')
    expect(mount(DiffView, { props: { files: [] } }).text()).toContain('No changes since last commit.')
  })
})
