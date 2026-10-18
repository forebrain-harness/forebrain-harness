import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import RadioField from './RadioField.vue'

/** The radio group: the label, the checked option and what a change emits. */
describe('RadioField', () => {
  beforeAll(() => setLocale('en'))

  const plain = [
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ]

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
    expect(wrapper.findAll('input[type="radio"]').map((input) => (input.element as HTMLInputElement).checked)).toEqual([
      false,
      true,
    ])
  })

  it('emits the option that was picked', async () => {
    const wrapper = mountField()
    await wrapper.findAll('input[type="radio"]')[0].setValue(true)
    expect(wrapper.emitted('update:modelValue')).toEqual([['light']])
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
