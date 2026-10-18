import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import SettingsApprovalTab from './SettingsApprovalTab.vue'

/** The global approval default: one vertical radio group and one save. */
describe('SettingsApprovalTab', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'approvalDefault').mockResolvedValue({ current: 'auto' })
    vi.spyOn(forebrainApi, 'saveApprovalDefault').mockResolvedValue({ current: 'full-access' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function mountTab() {
    const wrapper = mount(SettingsApprovalTab)
    await flushPromises()
    return wrapper
  }

  const saveButton = (wrapper: Awaited<ReturnType<typeof mountTab>>) => wrapper.find('[data-testid="approval-save"]')

  it('is a flat form: no title row, no card, no big preset buttons', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.find('h3').exists()).toBe(false)
    expect(wrapper.find('.forebrain-card').exists()).toBe(false)
    expect(wrapper.find('.approval-card').exists()).toBe(false)
  })

  it('runs the three presets down the page, each with its explanation', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-testid="approval-default"]')
    expect(group.attributes('role')).toBe('radiogroup')
    expect(group.find('.forebrain-radios').classes()).toContain('is-vertical')
    const radios = group.findAll('input[type="radio"]')
    expect(radios).toHaveLength(3)
    expect(radios.map((radio) => (radio.element as HTMLInputElement).checked)).toEqual([false, true, false])

    const labels = group.findAll('.forebrain-radio-desc b').map((label) => label.text())
    expect(labels).toEqual(['只读 · Read Only', '默认 · Default', '完全访问 · Full Access'])
    const descriptions = group.findAll('.forebrain-radio-desc span').map((text) => text.text())
    expect(descriptions[0]).toBe('Reads files in the current workspace; approval is required to edit.')
  })

  it('names the configuration keys in monospace on the help line', async () => {
    const wrapper = await mountTab()
    const note = wrapper.find('.forebrain-list-note')
    expect(note.text()).toContain('forebrain.yaml · approval_policy + sandbox_mode')
    expect(note.find('.font-mono').text()).toBe('forebrain.yaml · approval_policy + sandbox_mode')
    expect(note.text()).toContain('Applies to every new session of every primary agent')
  })

  it('keeps the save disabled until the choice changes', async () => {
    const wrapper = await mountTab()
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.findAll('input[type="radio"]')[2].setValue(true)
    expect(saveButton(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('saves the chosen preset and stays saved', async () => {
    const wrapper = await mountTab()
    await wrapper.findAll('input[type="radio"]')[2].setValue(true)
    await saveButton(wrapper).trigger('click')
    await flushPromises()

    expect(forebrainApi.saveApprovalDefault).toHaveBeenCalledWith('full-access')
    expect(wrapper.text()).toContain('Saved as the global default.')
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()
  })

  it('names a configuration that matches no preset', async () => {
    vi.mocked(forebrainApi.approvalDefault).mockResolvedValue({ current: null })
    const wrapper = await mountTab()
    expect(wrapper.text()).toContain('matches no preset')
    expect(wrapper.findAll('input[type="radio"]').some((radio) => (radio.element as HTMLInputElement).checked)).toBe(false)
  })
})
