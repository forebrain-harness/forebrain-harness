import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import SettingsSubagentsTab from './SettingsSubagentsTab.vue'

/** The global subagent switch, flat in its tab, saved from the bottom. */
describe('SettingsSubagentsTab', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'subagentDefaults').mockResolvedValue({ enabled: false })
    vi.spyOn(forebrainApi, 'setSubagentDefaults').mockResolvedValue({ enabled: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  async function mountTab() {
    const wrapper = mount(SettingsSubagentsTab)
    await flushPromises()
    return wrapper
  }

  const saveButton = (wrapper: Awaited<ReturnType<typeof mountTab>>) =>
    wrapper.find('[data-testid="subagents-feature-save"]')

  it('opens with the switch the file has, and saves nothing until it moves', async () => {
    const wrapper = await mountTab()

    expect(forebrainApi.subagentDefaults).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()
  })

  it('saves the toggled switch once, through the global default', async () => {
    const wrapper = await mountTab()

    await wrapper.find('[role="switch"]').trigger('click')
    expect(saveButton(wrapper).attributes('disabled')).toBeUndefined()
    await saveButton(wrapper).trigger('click')
    await flushPromises()

    expect(forebrainApi.setSubagentDefaults).toHaveBeenCalledExactlyOnceWith(true)
  })

  it('says what the switch covers and where it is written', async () => {
    const wrapper = await mountTab()

    expect(wrapper.text()).toContain('Enable subagents')
    expect(wrapper.text()).toContain('Applies to every primary agent')
    expect(wrapper.find('.forebrain-list-note').text()).toContain('agents.defaults.enable_subagent')
  })
})
