import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { currentLocale, setLocale } from '@/locales'
import AppearanceTab from './AppearanceTab.vue'

/** The appearance form: two standard radio groups, a language menu, no save. */
describe('AppearanceTab', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  /** The radios of one group, in the order the design draws them. */
  function radios(wrapper: ReturnType<typeof mount>, group: string) {
    return wrapper.find(`[data-testid="${group}"]`).findAll('input[type="radio"]')
  }

  it('is a flat form: no title row and no card around it', () => {
    const wrapper = mount(AppearanceTab)
    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.find('h3').exists()).toBe(false)
    expect(wrapper.find('.forebrain-card').exists()).toBe(false)
    expect(wrapper.find('.grid').classes()).toContain('gap-3')
  })

  it('offers the three brand colours as a standard radio group, each with its swatch', () => {
    const wrapper = mount(AppearanceTab)
    const group = wrapper.find('[data-testid="appearance-brand"]')
    expect(group.attributes('role')).toBe('radiogroup')
    expect(group.text()).toContain('Brand colour')
    expect(group.text()).toContain('Navy (default)')
    const inputs = radios(wrapper, 'appearance-brand')
    expect(inputs).toHaveLength(3)
    expect(wrapper.findAll('[data-testid="appearance-brand"] .forebrain-radio-swatch')).toHaveLength(3)
  })

  it('switches the brand on the spot, without a save button', async () => {
    const wrapper = mount(AppearanceTab)
    await radios(wrapper, 'appearance-brand')[1].setValue(true)
    expect(document.documentElement.dataset.brand).toBe('teal')
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('switches the menu bar between dark and light on the spot', async () => {
    const wrapper = mount(AppearanceTab)
    const inputs = radios(wrapper, 'appearance-rail')
    expect(inputs).toHaveLength(2)
    await inputs[1].setValue(true)
    expect(document.documentElement.dataset.railTheme).toBe('light')
  })

  it('offers the language the top bar offers, and changing it sets the locale', async () => {
    const wrapper = mount(AppearanceTab)
    const select = wrapper.find('[data-testid="appearance-language"]')
    expect((select.element as HTMLSelectElement).value).toBe('en')
    expect(select.findAll('option').map((option) => option.text())).toEqual(['中文', 'English'])

    await select.setValue('zh')
    expect(currentLocale.value).toBe('zh')
  })

  it('redraws its labels in the language just chosen', async () => {
    const wrapper = mount(AppearanceTab)
    await wrapper.find('[data-testid="appearance-language"]').setValue('zh')
    expect(wrapper.find('[data-testid="appearance-brand"]').text()).toContain('海军蓝（默认）')
    setLocale('en')
  })
})
