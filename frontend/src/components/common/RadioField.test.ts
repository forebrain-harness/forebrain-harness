import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import RadioField from './RadioField.vue'

/**
 * The radio group: the label, the checked option and what a change emits. The
 * options are the library's RadioGroup, so an option is a `role="radio"`
 * button rather than a native input — the keyboard is driven the way a
 * keyboard user drives it, and clicking is still one click.
 */
describe('RadioField', () => {
  beforeAll(() => setLocale('en'))

  // The keyboard case mounts attached to the document, because focus can only
  // move between options in a rendered document. Unmounting happens here so no
  // wrapper outlives its test.
  enableAutoUnmount(afterEach)

  const plain = [
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ]

  const three = [
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
    { value: 'system', label: 'System' },
  ]

  /** Let a focus event's watchers run and the DOM settle behind them. */
  async function settle(): Promise<void> {
    await flushPromises()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await flushPromises()
  }

  function mountField(props: Record<string, unknown> = {}) {
    return mount(RadioField, {
      props: { label: 'Theme', name: 'theme', options: plain, modelValue: 'dark', ...props },
    })
  }

  it('labels the group and checks the current option', () => {
    const wrapper = mountField()
    const group = wrapper.find('[role="radiogroup"]')
    expect(group.classes()).toContain('forebrain-form-field')
    const label = wrapper.find('.forebrain-form-field > span')
    expect(label.text()).toBe('Theme')
    expect(group.attributes('aria-labelledby')).toBe(label.attributes('id'))
    expect(wrapper.findAll('[role="radio"]').map((radio) => radio.attributes('aria-checked'))).toEqual([
      'false',
      'true',
    ])
  })

  it('emits the option that was picked', async () => {
    const wrapper = mountField()
    await wrapper.findAll('[role="radio"]')[0].trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['light']])
  })

  it('picks the second of three once, in a single click', async () => {
    const clicked = mountField({ options: three, modelValue: 'light' })
    await clicked.findAll('[role="radio"]')[1].trigger('click')
    // Once, not twice: the option's words are a label aimed at this button.
    expect(clicked.emitted('update:modelValue')).toEqual([['dark']])
  })

  it('moves to the next option on ArrowDown, once, and leaves focus on it', async () => {
    const wrapper = mount(RadioField, {
      props: { label: 'Theme', name: 'theme', options: three, modelValue: 'light' },
      attachTo: document.body,
    })
    const items = wrapper.findAll('[role="radio"]')
    const first = items[0].element as HTMLElement
    first.focus()
    // The library binds its key handler from the item's own focus event, so a
    // key sent in the same tick as focus() reaches nothing: let focus settle.
    await settle()
    expect(document.activeElement).toBe(first)

    const move = new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true })
    first.dispatchEvent(move)
    expect(move.defaultPrevented).toBe(true)
    await settle()

    // A real move: the model lands on the second option exactly once, and the
    // focus ring goes with it.
    expect(wrapper.emitted('update:modelValue')).toEqual([['dark']])
    expect(document.activeElement?.id).toBe('radio-group-theme-dark')
  })

  it('runs the options down the page when asked to', () => {
    expect(mountField().find('.forebrain-radios').classes()).not.toContain('is-vertical')
    expect(mountField({ vertical: true }).find('.forebrain-radios').classes()).toContain('is-vertical')
  })

  it('draws a swatch and a description when an option carries them', () => {
    const wrapper = mountField({
      options: [
        { value: 'brand', label: 'Brand', swatch: '#0d91b0', description: 'The product colour' },
        { value: 'plain', label: 'Plain' },
      ],
      hint: 'Only the accent changes',
    })
    const swatch = wrapper.find('.forebrain-radio-swatch')
    expect(swatch.attributes('style') ?? '').toMatch(/0d91b0|rgb\(13,\s*145,\s*176\)/)
    expect(wrapper.find('.forebrain-radio.has-desc .forebrain-radio-desc b').text()).toBe('Brand')
    expect(wrapper.find('.forebrain-radio-desc span').text()).toBe('The product colour')
    expect(wrapper.find('.forebrain-list-note').text()).toBe('Only the accent changes')
  })
})
