import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import tip from '@/directives/tip'
import forebrainApi from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'

import EnvVarsTab from './EnvVarsTab.vue'

/**
 * Settings · Variables (plan 266): the operator's own values, which MCP and LSP
 * entries read as `${NAME}`. A value never comes back — only the fact that one is
 * set and its last four characters — so an edit writes a new value, and a name
 * outside the rule the gateway enforces is refused before the write.
 */

beforeAll(() => setLocale('zh'))
afterEach(() => vi.restoreAllMocks())

function mountTab(): VueWrapper {
  return mount(EnvVarsTab, {
    attachTo: document.body,
    global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
}

describe('EnvVarsTab', () => {
  it('lists a variable by name, with its value masked to the last four', async () => {
    vi.spyOn(forebrainApi, 'envVarsPage').mockResolvedValue({
      rows: [{ name: 'GITHUB_TOKEN', valueSet: true, valueLast4: 'f9Qa', updatedAt: Date.UTC(2026, 10, 7, 10, 12) }],
      total: 1,
      page: 1,
      pageSize: 10,
    })

    const wrapper = mountTab()
    await flushPromises()

    expect(wrapper.find('[data-testid="vars-cell-name"]').text()).toBe('GITHUB_TOKEN')
    const value = wrapper.find('[data-testid="vars-cell-value"]').text()
    expect(value).toContain('f9Qa')
    expect(value).not.toContain('secret')
    expect(wrapper.text()).toContain('MCP、LSP 配置里的 ${名称} 从这里取值')
  })

  it('sets a value through the dialog, and says which names the gateway accepts', async () => {
    vi.spyOn(forebrainApi, 'envVarsPage').mockResolvedValue({ rows: [], total: 0, page: 1, pageSize: 10 })
    const set = vi.spyOn(forebrainApi, 'setEnvVar').mockResolvedValue({ name: 'NEW_TOKEN' })

    const wrapper = mountTab()
    await flushPromises()
    await wrapper.find('[data-testid="vars-add"]').trigger('click')
    await flushPromises()

    // A name the gateway would refuse is refused here, under the box.
    await wrapper.find('[data-testid="vars-name"]').setValue('1bad name')
    expect(wrapper.find('[data-testid="vars-name-error"]').exists()).toBe(true)

    await wrapper.find('[data-testid="vars-name"]').setValue('NEW_TOKEN')
    await wrapper.find('[data-testid="vars-value"]').setValue('s3cret-value')
    await wrapper.find('[data-testid="vars-save"]').trigger('click')
    await flushPromises()

    expect(set).toHaveBeenCalledWith('NEW_TOKEN', 's3cret-value')
  })

  it('cannot save an empty value: “nothing” is the delete button', async () => {
    vi.spyOn(forebrainApi, 'envVarsPage').mockResolvedValue({ rows: [], total: 0, page: 1, pageSize: 10 })
    const wrapper = mountTab()
    await flushPromises()

    await wrapper.find('[data-testid="vars-add"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="vars-name"]').setValue('NEW_TOKEN')

    expect(wrapper.find('[data-testid="vars-save"]').attributes('disabled')).toBeDefined()
  })

  it('deletes a variable after the confirmation', async () => {
    vi.spyOn(forebrainApi, 'envVarsPage').mockResolvedValue({
      rows: [{ name: 'JIRA_API_TOKEN', valueSet: true, valueLast4: '2kLm', updatedAt: 0 }],
      total: 1,
      page: 1,
      pageSize: 10,
    })
    const remove = vi.spyOn(forebrainApi, 'deleteEnvVar').mockResolvedValue(undefined)

    const wrapper = mountTab()
    await flushPromises()
    await wrapper.find('[data-testid="vars-remove-JIRA_API_TOKEN"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()

    expect(remove).toHaveBeenCalledWith('JIRA_API_TOKEN')
  })
})
