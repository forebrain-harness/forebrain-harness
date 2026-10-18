import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { hideTip, useTooltipState } from '@/composables/useTooltip'
import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import SortHeader from './SortHeader.vue'

/** A sortable column header: the first click, the turn-around and the aria state. */
describe('SortHeader', () => {
  beforeAll(() => setLocale('en'))
  beforeEach(() => hideTip())

  function mountHeader(
    props: Partial<{ label: string; sortKey: string; active: boolean; order: 'asc' | 'desc'; first: 'asc' | 'desc' }> = {},
  ) {
    return mount(SortHeader, {
      props: { label: 'Updated', sortKey: 'updated', active: false, order: 'desc', first: 'desc', ...props },
      global: { directives: { tip } },
    })
  }

  it('sorts by the column default on the first click', async () => {
    const time = mountHeader({ first: 'desc' })
    await time.find('.forebrain-th-sort').trigger('click')
    expect(time.emitted('sort')).toEqual([['updated', 'desc']])

    const name = mountHeader({ first: 'asc', sortKey: 'name', label: 'Name' })
    await name.find('.forebrain-th-sort').trigger('click')
    expect(name.emitted('sort')).toEqual([['name', 'asc']])
  })

  it('turns the order around once the column is sorted', async () => {
    const wrapper = mountHeader({ active: true, order: 'desc' })
    await wrapper.find('.forebrain-th-sort').trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['updated', 'asc']])
  })

  it('says which column is sorted and which way', () => {
    expect(mountHeader().find('th').attributes('aria-sort')).toBe('none')
    expect(mountHeader({ active: true, order: 'asc' }).find('th').attributes('aria-sort')).toBe('ascending')
    const descending = mountHeader({ active: true, order: 'desc' })
    expect(descending.find('th').attributes('aria-sort')).toBe('descending')
    expect(descending.find('th').classes()).toContain('is-on')
    expect(descending.findAll('.forebrain-sort-ic path.is-act')).toHaveLength(1)
  })

  it('names the next click in its hint, as the app\'s tip rather than a native title', async () => {
    const first = mountHeader({ first: 'desc' })
    expect(first.find('.forebrain-th-sort').attributes('title')).toBeUndefined()
    await first.find('.forebrain-th-sort').trigger('mouseenter')
    expect(useTooltipState().text).toBe('Sort by Updated (newest first)')

    const name = mountHeader({ first: 'asc', label: 'Name' })
    await name.find('.forebrain-th-sort').trigger('mouseenter')
    expect(useTooltipState().text).toBe('Sort by Name (A→Z)')

    const sorted = mountHeader({ active: true, order: 'desc', first: 'desc' })
    await sorted.find('.forebrain-th-sort').trigger('mouseenter')
    expect(useTooltipState().text).toBe('Sort ascending (oldest first)')
  })
})
