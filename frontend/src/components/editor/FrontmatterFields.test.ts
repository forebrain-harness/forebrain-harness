import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import FrontmatterFields from './FrontmatterFields.vue'

// The front-matter block is edited as fields but written back as YAML. Nothing
// the user did not touch may move, or opening a SKILL.md and saving would
// reformat it for no reason.
describe('FrontmatterFields', () => {
  it('renders one field per keyed line', () => {
    const wrapper = mount(FrontmatterFields, {
      props: { modelValue: 'name: gateway-ops\ndescription: 网关排障\n' },
    })
    const inputs = wrapper.findAll('input')
    expect(inputs.map((input) => (input.element as HTMLInputElement).value)).toEqual(['gateway-ops', '网关排障'])
  })

  // The fields are the library's Input, whose model update lands on the next
  // flush: `setValue` is awaited so the write-back has happened when it is read.
  it('writes back the edited field and leaves the rest byte-for-byte', async () => {
    const wrapper = mount(FrontmatterFields, {
      props: { modelValue: 'name: gateway-ops\ndescription: 网关排障\n' },
    })
    await wrapper.findAll('input')[1].setValue('网关排障与恢复')
    const emitted = wrapper.emitted('update:modelValue') ?? []
    expect(emitted[emitted.length - 1]).toEqual(['name: gateway-ops\ndescription: 网关排障与恢复\n'])
  })

  it('keeps lines it cannot key verbatim', async () => {
    const wrapper = mount(FrontmatterFields, {
      props: { modelValue: '# a comment\nname: demo\n\n' },
    })
    // Only `name` is a field; the comment and the blank line are shown as-is.
    const inputs = wrapper.findAll('input')
    expect(inputs).toHaveLength(1)
    await inputs[0].setValue('demo2')
    const emitted = wrapper.emitted('update:modelValue') ?? []
    expect(emitted[emitted.length - 1]).toEqual(['# a comment\nname: demo2\n\n'])
  })
})
