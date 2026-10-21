import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { h, nextTick } from 'vue'

import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import DataTable, { type DataColumn, type TableSorting } from './DataTable.vue'

/**
 * The table every list draws (plan 084 pilot): shadcn-vue's table primitives and
 * the design's own classes, with column widths measured here and sorting left to
 * the server. jsdom has no layout — no widths, no content measurement — so what
 * this file pins is the state and the markup: which width each column is given,
 * what a click emits, and what a cell slot draws. The pixels belong to the e2e
 * suite.
 */

type Service = { id: string; name: string }

const rows: Service[] = [
  { id: 'a', name: 'deepseek' },
  { id: 'b', name: 'openai' },
]

const columns: DataColumn[] = [
  { id: 'name', header: 'Service', size: 140 },
  { id: 'note', header: 'Note' },
]

async function mountTable(
  overrides: {
    columns?: DataColumn[]
    rows?: Service[]
    sorting?: TableSorting
  } = {},
) {
  const wrapper = mount(DataTable, {
    props: {
      columns,
      rows,
      rowKey: (row: unknown) => (row as Service).id,
      ...overrides,
    },
    global: { directives: { tip } },
    slots: {
      'cell-name': ({ row }: { row: unknown }) => h('span', { class: 'cell-name' }, (row as Service).name),
      'cell-note': () => h('span', { class: 'cell-note' }, '—'),
    },
  })
  // The first layout is measured after the table mounts, so the widths below are
  // the second render's.
  await nextTick()
  return wrapper
}

/** Pretend the browser laid the table out this wide. */
function containerWidth(width: number): void {
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', { configurable: true, get: () => width })
}

describe('DataTable', () => {
  beforeAll(() => setLocale('en'))

  afterEach(() => {
    delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth
  })

  it('draws the design table: the boxed wrap, the fixed table and one col per column', async () => {
    const wrapper = await mountTable()

    const table = wrapper.find('.forebrain-table-wrap table.forebrain-table.forebrain-table--fixed')
    expect(table.exists()).toBe(true)
    expect(wrapper.findAll('col')).toHaveLength(2)
    expect(wrapper.findAll('thead th').map((th) => th.text())).toEqual(['Service', 'Note'])
  })

  it('gives a column without a width the room the others leave', async () => {
    // 600px of container, 140px declared: the flexible column takes the rest, so
    // the number a drag starts from is the number on screen.
    containerWidth(600)
    const wrapper = await mountTable()

    const [first, second] = wrapper.findAll('col').map((col) => col.attributes('style') ?? '')
    expect(first).toContain('width: 140px')
    expect(second).toContain('width: 460px')
    expect(wrapper.find('table').attributes('style')).toContain('width: 600px')
  })

  it('keeps the declared widths when the container is too narrow, and scrolls instead', async () => {
    containerWidth(120)
    const wrapper = await mountTable()

    const [first, second] = wrapper.findAll('col').map((col) => col.attributes('style') ?? '')
    expect(first).toContain('width: 140px')
    // Below the minimum the column stops shrinking: the table scrolls.
    expect(second).toContain('width: 56px')
  })

  it('pins nothing on a container that has not been laid out yet', async () => {
    // A table mounted in a tab nobody has opened measures 0px. Answering with
    // widths would fold every flexible column down to its minimum and keep it
    // there; leaving them alone lets the browser lay the table out when it
    // becomes visible, which is what a hand-written table did.
    const wrapper = await mountTable()

    const [, second] = wrapper.findAll('col').map((col) => col.attributes('style') ?? '')
    expect(second).toContain('width: 150px')
  })

  it('draws each cell through its own slot, with the row it belongs to', async () => {
    const wrapper = await mountTable()

    expect(wrapper.findAll('tbody .cell-name').map((cell) => cell.text())).toEqual(['deepseek', 'openai'])
    expect(wrapper.findAll('tbody .cell-note')).toHaveLength(2)
  })

  it('carries the row attributes the caller needs to find a row', () => {
    const wrapper = mount(DataTable, {
      props: {
        columns,
        rows,
        rowKey: (row: unknown) => (row as Service).id,
        rowAttrs: (row: unknown) => ({ 'data-service-row': (row as Service).name }),
      },
      global: { directives: { tip } },
    })

    expect(wrapper.findAll('[data-service-row]').map((row) => row.attributes('data-service-row'))).toEqual([
      'deepseek',
      'openai',
    ])
  })

  it('offers no sort control unless a column asks for one', async () => {
    // Most list endpoints ignore `sort`, so the table must not pretend.
    const wrapper = await mountTable()

    expect(wrapper.find('.forebrain-th-sort').exists()).toBe(false)
    expect(wrapper.findAll('thead th').map((th) => th.attributes('aria-sort'))).toEqual(['none', 'none'])
  })

  it('emits the column and the direction on a sort click, and shows the held one', async () => {
    const sortable: DataColumn[] = [
      { id: 'name', header: 'Service', enableSorting: true },
      { id: 'note', header: 'Note' },
    ]
    const wrapper = await mountTable({ columns: sortable })

    await wrapper.find('.forebrain-th-sort').trigger('click')
    expect(wrapper.emitted('update:sorting')).toEqual([[{ key: 'name', order: 'asc' }]])

    // The state is the server's: the arrow and `aria-sort` only ever follow the
    // prop, never a click the caller has not answered yet.
    await wrapper.setProps({ sorting: { key: 'name', order: 'desc' } })
    expect(wrapper.find('thead th').attributes('aria-sort')).toBe('descending')
    await wrapper.setProps({ sorting: { key: 'name', order: 'asc' } })
    expect(wrapper.find('thead th').attributes('aria-sort')).toBe('ascending')
  })

  it('starts on the direction the column declares', async () => {
    const wrapper = await mountTable({
      columns: [{ id: 'created', header: 'Created', enableSorting: true, sortDescFirst: true }],
    })

    await wrapper.find('.forebrain-th-sort').trigger('click')
    expect(wrapper.emitted('update:sorting')).toEqual([[{ key: 'created', order: 'desc' }]])
  })

  it('puts a width handle on every column', async () => {
    const wrapper = await mountTable()
    expect(wrapper.findAll('thead [data-col-resizer]')).toHaveLength(2)
  })

  it('fits a column to its content when the handle is double-clicked', async () => {
    // jsdom measures nothing, so the widest cell is 0 and the answer is the
    // column's minimum — what matters here is that the double-click reaches the
    // measurement rather than being swallowed by the drag.
    const wrapper = await mountTable()
    await wrapper.find('thead [data-col-resizer]').trigger('dblclick')

    const [first] = wrapper.findAll('col').map((col) => col.attributes('style') ?? '')
    expect(first).toContain('width: 56px')
  })

  it('draws the caller’s empty state across the whole table', () => {
    const wrapper = mount(DataTable, {
      props: { columns, rows: [], rowKey: (row) => (row as Service).id },
      global: { directives: { tip } },
      slots: { empty: () => h('span', { class: 'no-rows' }, 'No services yet') },
    })

    const cell = wrapper.find('tbody td')
    expect(cell.attributes('colspan')).toBe('2')
    expect(cell.find('.no-rows').text()).toBe('No services yet')
  })
})
