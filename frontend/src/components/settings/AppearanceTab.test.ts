import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import { currentLocale, setLocale } from '@/locales'
import { openSelect, optionTexts, pickOption, selectedText } from '@/test-utils/formControls'
import AppearanceTab from './AppearanceTab.vue'

// The language select is the library's, so its options render in a portal on
// the document rather than inside the wrapper. A wrapper left mounted would
// keep them there for the next test's document-wide lookups.
enableAutoUnmount(afterEach)

/** The appearance form: two standard radio groups, a language menu, no save. */
describe('AppearanceTab', () => {
  beforeAll(() => {
    setLocale('en')
    // The select measures its viewport with ResizeObserver, which jsdom does
    // not have; scrolling an item into view is not implemented either.
    ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    Element.prototype.scrollIntoView = () => undefined
  })
  afterAll(() => setLocale('en'))

  /** The radios of one group, in the order the design draws them. The group is
   * the library's RadioGroup, so an option is a `role="radio"` button. */
  function radios(wrapper: ReturnType<typeof mount>, group: string) {
    return wrapper.find(`[data-testid="${group}"]`).findAll('[role="radio"]')
  }

  /** The language select's trigger — the element that opens it. */
  function languageTrigger(wrapper: ReturnType<typeof mount>): HTMLElement {
    return wrapper.find('[data-testid="appearance-language"]').element as HTMLElement
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
    await radios(wrapper, 'appearance-brand')[1].trigger('click')
    expect(document.documentElement.dataset.brand).toBe('teal')
    // The radio options are the library's buttons now, and the language field
    // is the library's select trigger; what this pins is that nothing else in
    // the form is a button — there is no save.
    expect(wrapper.findAll('button:not([role="radio"]):not([role="combobox"])')).toHaveLength(0)
  })

  it('switches the menu bar between dark and light on the spot', async () => {
    const wrapper = mount(AppearanceTab)
    const inputs = radios(wrapper, 'appearance-rail')
    expect(inputs).toHaveLength(2)
    await inputs[1].trigger('click')
    expect(document.documentElement.dataset.railTheme).toBe('light')
  })

  it('offers the language the top bar offers, and changing it sets the locale', async () => {
    const wrapper = mount(AppearanceTab)
    await flushPromises()
    const language = languageTrigger(wrapper)
    expect(selectedText(language)).toBe('English')

    await openSelect(language)
    expect(optionTexts()).toEqual(['中文', 'English'])

    await pickOption(language, '中文')
    expect(currentLocale.value).toBe('zh')
  })

  it('redraws its labels in the language just chosen', async () => {
    const wrapper = mount(AppearanceTab)
    await flushPromises()
    await pickOption(languageTrigger(wrapper), '中文')
    expect(wrapper.find('[data-testid="appearance-brand"]').text()).toContain('海军蓝（默认）')
    setLocale('en')
  })
})
