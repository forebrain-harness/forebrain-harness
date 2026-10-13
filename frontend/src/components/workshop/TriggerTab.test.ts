import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import TriggerTab from './TriggerTab.vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

const skillMd = '---\nname: gateway-ops\ndescription: old description\n---\n\nbody\n'

describe('TriggerTab', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillWorkshopTrigger').mockResolvedValue({
      name: 'gateway-ops',
      queries: [{ query: 'gateway 502', shouldTrigger: true }],
      results: {
        currentDescription: 'old description',
        currentScore: 0.7,
        bestDescription: 'new description',
        bestScore: 0.92,
        queries: [{ query: 'gateway 502', shouldTrigger: true, pass: true }],
      },
    })
    vi.spyOn(forebrainApi, 'skillFileRead').mockResolvedValue({ name: 'gateway-ops', path: 'SKILL.md', content: skillMd })
    vi.spyOn(forebrainApi, 'skillFileWrite').mockResolvedValue({ ok: true, path: 'SKILL.md' })
  })
  afterEach(() => vi.restoreAllMocks())

  it('applies the suggested description to the draft frontmatter', async () => {
    const wrapper = mount(TriggerTab, { props: { skillName: 'gateway-ops', editable: true }, global: { stubs: { Teleport: true } } })
    await flush()
    expect(wrapper.find('[data-testid="workshop-trigger-desc-edit"]').exists()).toBe(true)
    await wrapper.find('[data-testid="workshop-trigger-apply"]').trigger('click')
    await flush()
    const write = vi.mocked(forebrainApi.skillFileWrite)
    expect(write).toHaveBeenCalledWith('gateway-ops', 'SKILL.md', expect.stringContaining('description: new description'))
  })
})
