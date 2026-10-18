import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import TablePager from './TablePager.vue'

/** The pager under every table: the totals, the page numbers and its own size select. */
describe('TablePager', () => {
  beforeAll(() => setLocale('en'))

  function mountPager(props: Partial<{ total: number; page: number; pageSize: number }> = {}) {
    return mount(TablePager, { props: { total: 25, page: 1, pageSize: 10, ...props } })
  }

  /** The page buttons in the order they are drawn. */
  function pageNumbers(wrapper: ReturnType<typeof mountPager>) {
    return wrapper.findAll('[data-testid^="pager-page-"]').map((button) => button.text())
  }

  it('lists every page when there are few', () => {
    const wrapper = mountPager({ total: 25 })
    expect(pageNumbers(wrapper)).toEqual(['1', '2', '3'])
    expect(wrapper.findAll('.forebrain-pg-gap')).toHaveLength(0)
    expect(wrapper.find('[data-testid="pager-total"]').text()).toBe('25 items')
  })

  it('collapses the pages around the current one when there are many', () => {
    const wrapper = mountPager({ total: 200, page: 10 })
    expect(pageNumbers(wrapper)).toEqual(['1', '9', '10', '11', '20'])
    expect(wrapper.findAll('.forebrain-pg-gap')).toHaveLength(2)
    const current = wrapper.find('[data-testid="pager-page-10"]')
    expect(current.attributes('aria-current')).toBe('page')
  })

  it('disables the step that would leave the list', () => {
    const first = mountPager({ total: 25, page: 1 })
    expect((first.find('[data-testid="pager-prev"]').element as HTMLButtonElement).disabled).toBe(true)
    expect((first.find('[data-testid="pager-next"]').element as HTMLButtonElement).disabled).toBe(false)
    const last = mountPager({ total: 25, page: 3 })
    expect((last.find('[data-testid="pager-prev"]').element as HTMLButtonElement).disabled).toBe(false)
    expect((last.find('[data-testid="pager-next"]').element as HTMLButtonElement).disabled).toBe(true)
  })

  it('asks for the page that was clicked', async () => {
    const wrapper = mountPager({ total: 200, page: 10 })
    await wrapper.find('[data-testid="pager-page-11"]').trigger('click')
    expect(wrapper.emitted('update:page')).toEqual([[11]])
  })

  it('asks for the page size that was picked', async () => {
    const wrapper = mountPager({ total: 200, page: 10 })
    await wrapper.find('[data-testid="pager-size"]').setValue('20')
    expect(wrapper.emitted('update:pageSize')).toEqual([[20]])
    expect(wrapper.findAll('[data-testid="pager-size"] option').map((option) => option.text())).toEqual([
      '10 / page',
      '20 / page',
      '50 / page',
    ])
  })

  it('keeps a page size the select does not offer', () => {
    const wrapper = mountPager({ total: 200, pageSize: 30 })
    expect(wrapper.findAll('[data-testid="pager-size"] option').map((option) => option.text())).toEqual([
      '10 / page',
      '20 / page',
      '30 / page',
      '50 / page',
    ])
  })

  it('still renders one page for an empty list', () => {
    const wrapper = mountPager({ total: 0, page: 1 })
    expect(wrapper.find('[data-testid="pager-total"]').text()).toBe('0 items')
    expect(pageNumbers(wrapper)).toEqual(['1'])
    expect(wrapper.findAll('.forebrain-pg-gap')).toHaveLength(0)
  })
})
