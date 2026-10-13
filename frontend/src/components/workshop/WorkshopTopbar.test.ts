import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import WorkshopTopbar from './WorkshopTopbar.vue'
import { setLocale } from '@/locales'

setLocale('zh')

function mountTopbar(props: Record<string, unknown>) {
  return mount(WorkshopTopbar, {
    props: {
      part: 'trailing',
      label: 'gateway-ops',
      hasSkill: true,
      version: 3,
      state: 'published',
      scope: 'agent',
      workbenchOpen: false,
      publishVersion: '4',
      canPublish: true,
      ...props,
    },
    global: { stubs: { SessionHeartbeat: true, Teleport: true } },
  })
}

describe('WorkshopTopbar', () => {
  it('disables publish when there is nothing to publish', async () => {
    const wrapper = mountTopbar({ part: 'trailing', canPublish: false })
    const publish = wrapper.find('[data-testid="workshop-publish"]')
    expect(publish.attributes('disabled')).toBeDefined()
    expect(publish.attributes('title')).toBe('没有未发布的修改')
  })

  it('enables publish with the next version when the draft differs', async () => {
    const wrapper = mountTopbar({ part: 'trailing', canPublish: true, publishVersion: '4' })
    const publish = wrapper.find('[data-testid="workshop-publish"]')
    expect(publish.attributes('disabled')).toBeUndefined()
    expect(publish.text()).toContain('发布 v4')
    await publish.trigger('click')
    expect(wrapper.emitted('publish')).toBeTruthy()
  })

  it('shows the version chip in the leading half', () => {
    const wrapper = mountTopbar({ part: 'leading', state: 'pending', version: 3 })
    expect(wrapper.find('[data-testid="workshop-crumb-leaf"]').text()).toBe('gateway-ops')
    expect(wrapper.text()).toContain('v3')
    expect(wrapper.text()).toContain('有未发布的修改')
  })
})
