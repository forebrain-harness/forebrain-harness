import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { forebrainApi, type MemorySettings } from '@/lib/api'
import { setLocale } from '@/locales'
import MemorySwitchesTab from './MemorySwitchesTab.vue'

/** The three global memory switches, flat in their tab, saved from the bottom. */
describe('MemorySwitchesTab', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  const off: MemorySettings = { enabled: false, useMemories: false, generateMemories: false }

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'memorySettings').mockResolvedValue({ ...off })
    vi.spyOn(forebrainApi, 'updateMemorySettings').mockResolvedValue({ enabled: true, useMemories: false, generateMemories: false })
    vi.spyOn(forebrainApi, 'resetMemories').mockResolvedValue({ ok: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  async function mountTab() {
    const wrapper = mount(MemorySwitchesTab)
    await flushPromises()
    return wrapper
  }

  const saveButton = (wrapper: Awaited<ReturnType<typeof mountTab>>) => wrapper.find('[data-testid="memory-switches-save"]')

  it('is a flat form: no title row, no card, no frame around the switches', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.find('h3').exists()).toBe(false)
    expect(wrapper.find('.forebrain-card').exists()).toBe(false)
    expect(wrapper.find('.forebrain-list-boxed').exists()).toBe(false)
  })

  it('draws the three switches as rows, each with its title and hint', async () => {
    const wrapper = await mountTab()
    const rows = wrapper.findAll('[data-testid^="memory-switch-"]')
    expect(rows).toHaveLength(3)
    expect(rows[0].text()).toContain('Enable memories')
    expect(rows[0].text()).toContain('Turns on memory capabilities')
    expect(rows[2].text()).toContain('Generate memories')
    expect(wrapper.findAll('[role="switch"]')).toHaveLength(3)
  })

  it('explains what the switches cover, under the rows', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('.forebrain-list-note').text()).toContain('Applies to every primary agent')
  })

  it('saves all three from the bottom button, which waits for a change', async () => {
    const wrapper = await mountTab()
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.findAll('[role="switch"]')[0].trigger('click')
    expect(saveButton(wrapper).attributes('disabled')).toBeUndefined()
    await saveButton(wrapper).trigger('click')
    await flushPromises()

    expect(forebrainApi.updateMemorySettings).toHaveBeenCalledWith({
      featureEnabled: true,
      useMemories: false,
      generateMemories: false,
    })
    expect(wrapper.text()).toContain('Memory settings saved.')
  })

  it('keeps the reset beside the save, behind its confirmation', async () => {
    const wrapper = await mountTab()
    const reset = wrapper.find('[data-testid="memory-switches-reset"]')
    expect(reset.classes()).toContain('forebrain-btn--danger')

    vi.stubGlobal('confirm', vi.fn(() => true))
    await reset.trigger('click')
    await flushPromises()
    expect(forebrainApi.resetMemories).toHaveBeenCalledTimes(1)
  })
})
