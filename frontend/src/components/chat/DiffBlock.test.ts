import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { setLocale } from '@/locales'
import DiffBlock from './DiffBlock.vue'
import type { DiffRow } from '@/lib/unifiedDiff'

config.global.directives = { tip }

const rows: DiffRow[] = [
  { mark: ' ', line: 10, text: 'context' },
  { mark: '-', line: 11, text: 'gone' },
  { mark: '+', line: 11, text: 'here' },
  { mark: '…', line: null, text: '' },
  { mark: '+', line: 30, text: 'far' },
]

describe('DiffBlock', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => setLocale('zh'))

  it('marks added and removed rows with the bar classes and keeps the fold plain', () => {
    const wrapper = mount(DiffBlock, { props: { rows } })
    const marks = wrapper.findAll('.dr')
    expect(marks.map((row) => row.classes())).toEqual([
      ['dr'], ['dr', 'del'], ['dr', 'add'], ['dr', 'gap'], ['dr', 'add'],
    ])
    // The line-number column is the new file’s for context and added rows,
    // the old file’s for removed ones; the fold carries none.
    expect(marks.map((row) => row.find('.g').text())).toEqual(['10', '11', '11', '', '30'])
    expect(marks[3]!.find('.t').text()).toBe('⋯')
  })

  it('says 新增 / 删除 for screen readers ahead of the marked text', () => {
    const wrapper = mount(DiffBlock, { props: { rows } })
    const sr = wrapper.findAll('.sr-only').map((span) => span.text())
    expect(sr).toEqual(['删除', '新增', '新增'])
  })

  it('draws a new file as plain code: no add rows, no bars, numbers kept', () => {
    const wrapper = mount(DiffBlock, { props: { rows, plain: true } })
    expect(wrapper.findAll('.dr.add')).toHaveLength(0)
    expect(wrapper.findAll('.dr.del')).toHaveLength(0)
    // The fold flattens with everything else, the way the prototype’s plain
    // path re-marks every row: a created file is one hunk and has no fold.
    expect(wrapper.findAll('.dr.gap')).toHaveLength(0)
    expect(wrapper.findAll('.g').map((g) => g.text())).toEqual(['10', '11', '11', '', '30'])
    expect(wrapper.text()).toContain('here')
  })
})
