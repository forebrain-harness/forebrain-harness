import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import PublishDialog from './PublishDialog.vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

describe('PublishDialog', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillDiff').mockResolvedValue({
      name: 'gateway-ops',
      from: 'v3',
      to: 'draft',
      files: [{ path: 'SKILL.md', status: 'modified', added: 6, removed: 2 }],
    })
    vi.spyOn(forebrainApi, 'skillPublish').mockResolvedValue({ status: 'ok', version: 4 })
  })
  afterEach(() => vi.restoreAllMocks())

  it('names the next version, lists the changes and states the session rule', async () => {
    const wrapper = mount(PublishDialog, {
      props: { open: true, skillName: 'gateway-ops', currentVersion: 3 },
      global: { stubs: { Teleport: InlineTeleport } },
    })
    await flush()
    expect(wrapper.text()).toContain('发布 gateway-ops v4')
    expect(wrapper.text()).toContain('SKILL.md')
    expect(wrapper.text()).toContain('新会话使用 v4；进行中的会话继续用 v3。')
    await wrapper.find('[data-testid="workshop-publish-confirm"]').trigger('click')
    await flush()
    expect(wrapper.emitted('published')?.[0]?.[0]).toBe(4)
  })

  it('says the first publish enables the skill for new sessions', async () => {
    const wrapper = mount(PublishDialog, {
      props: { open: true, skillName: 'gateway-5xx', currentVersion: 0 },
      global: { stubs: { Teleport: InlineTeleport } },
    })
    await flush()
    expect(wrapper.text()).toContain('发布 gateway-5xx v1')
    expect(wrapper.text()).toContain('发布后默认启用，新会话生效。')
  })
})
