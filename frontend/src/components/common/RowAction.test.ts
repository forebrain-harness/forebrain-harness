import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { Pencil, Trash2 } from 'lucide-vue-next'

import { hideTip, useTooltipState } from '@/composables/useTooltip'
import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import RowAction from './RowAction.vue'
import RowActionSlot from './RowActionSlot.vue'
import RowActions from './RowActions.vue'

/** One row action: its name, its tone and the tip it shows on hover. */
describe('RowAction', () => {
  beforeAll(() => setLocale('en'))
  beforeEach(() => hideTip())

  function mountAction(props: Record<string, unknown> = {}) {
    return mount(RowAction, {
      props: { icon: Pencil, label: 'Edit', ...props },
      global: { directives: { tip } },
    })
  }

  it('names itself with the row it belongs to', () => {
    expect(mountAction().find('button').attributes('aria-label')).toBe('Edit')
    expect(mountAction({ name: 'Memory files' }).find('button').attributes('aria-label')).toBe('Edit Memory files')
  })

  it('tints a destructive action and leaves a plain one alone', () => {
    const danger = mountAction({ icon: Trash2, label: 'Delete', tone: 'danger' })
    expect(danger.find('button').classes()).toContain('forebrain-row-action--danger')
    expect(danger.find('button').classes()).toContain('forebrain-icon-btn')
    expect(danger.find('button').attributes('title')).toBeUndefined()
    const ok = mountAction({ label: 'Allow', tone: 'ok' })
    expect(ok.find('button').classes()).toContain('forebrain-row-action--ok')
    expect(mountAction().find('button').classes()).toEqual(['forebrain-icon-btn', 'forebrain-row-action'])
  })

  it('shows its label as a tip on hover, above the button', async () => {
    const wrapper = mountAction({ icon: Trash2, label: 'Delete' })
    expect(useTooltipState().text).toBe('')
    await wrapper.find('button').trigger('mouseenter')
    expect(useTooltipState().text).toBe('Delete')
    expect(useTooltipState().placement).toBe('top')
    expect(useTooltipState().anchor).toBe(wrapper.find('button').element)
    await wrapper.find('button').trigger('mouseleave')
    expect(useTooltipState().text).toBe('')
  })

  it('passes its listeners and test id to the button', () => {
    const wrapper = mountAction({ 'data-testid': 'row-delete' })
    expect(wrapper.find('[data-testid="row-delete"]').exists()).toBe(true)
  })
})

describe('RowActions', () => {
  it('holds the slot in a right-aligned row', () => {
    const wrapper = mount(RowActions, { slots: { default: '<i class="probe" />' } })
    expect(wrapper.find('.forebrain-row-actions .probe').exists()).toBe(true)
  })
})

describe('RowActionSlot', () => {
  it('draws an empty 28px hole', () => {
    const wrapper = mount(RowActionSlot)
    expect(wrapper.find('span').classes()).toContain('forebrain-icon-slot')
    expect(wrapper.find('span').attributes('aria-hidden')).toBe('true')
  })
})
