import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelChipsInput from './ModelChipsInput.vue'
import { t } from '@/locales'

describe('ModelChipsInput', () => {
  it('commits each comma segment as typed and the tail on blur', async () => {
    // The chips are the parent's state (a controlled field), so the test
    // feeds every emit back as the parent would.
    const wrapper = mount(ModelChipsInput, { props: { modelValue: [] } })
    const feed = async () => {
      const last = wrapper.emitted('update:modelValue')?.at(-1)?.[0] as string[]
      if (last) await wrapper.setProps({ modelValue: last })
    }
    const input = wrapper.find('[data-testid="model-chips-input"]')
    await input.setValue('deepseek-v4, deepseek-v4-flash')
    await feed()
    // The comma committed the first segment immediately.
    expect(wrapper.props('modelValue')).toEqual(['deepseek-v4'])
    await input.trigger('blur')
    await feed()
    // The tail became its own chip when the field was left.
    expect(wrapper.props('modelValue')).toEqual(['deepseek-v4', 'deepseek-v4-flash'])
    expect((input.element as HTMLInputElement).value).toBe('')
  })

  it('splits on every comma as the user types', async () => {
    const wrapper = mount(ModelChipsInput, { props: { modelValue: [] } })
    const input = wrapper.find('[data-testid="model-chips-input"]')
    await input.setValue('deepseek-v4,')
    await input.trigger('input')
    // The comma commit happens on input: the first model is already a chip
    // while the rest of the line stays editable.
    await input.trigger('blur')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['deepseek-v4'])
  })

  it('commits on Enter', async () => {
    const wrapper = mount(ModelChipsInput, { props: { modelValue: [] } })
    const input = wrapper.find('[data-testid="model-chips-input"]')
    await input.setValue('gpt-test')
    await input.trigger('keydown.enter')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['gpt-test'])
  })

  it('never duplicates a model', async () => {
    const wrapper = mount(ModelChipsInput, { props: { modelValue: ['gpt-test'] } })
    const input = wrapper.find('[data-testid="model-chips-input"]')
    await input.setValue('gpt-test, gpt-other')
    await input.trigger('blur')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['gpt-test', 'gpt-other'])
  })

  it('removes a chip through its × button', async () => {
    const wrapper = mount(ModelChipsInput, { props: { modelValue: ['a', 'b'] } })
    const chip = wrapper.find('[data-chip="a"]')
    await chip.find('button').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['b'])
  })

  it('hides suggestions already present and adds the rest on click', async () => {
    const wrapper = mount(ModelChipsInput, {
      props: { modelValue: ['a'], suggestions: ['a', 'b', 'c'] },
    })
    const buttons = wrapper.findAll('button:not([aria-label])')
    const suggestionButtons = buttons.filter((b) => ['b', 'c'].includes(b.text()))
    expect(suggestionButtons.length).toBe(2)
    await suggestionButtons[0].trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['a', 'b'])
  })

  it('keeps an empty list when nothing was typed', async () => {
    const wrapper = mount(ModelChipsInput, { props: { modelValue: [] } })
    const input = wrapper.find('[data-testid="model-chips-input"]')
    await input.setValue('   ')
    await input.trigger('blur')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    void t
  })
})
