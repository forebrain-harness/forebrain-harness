import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { mountSettingsTab } from './form/settingsTestHost'
import CronSettingsTab from './CronSettingsTab.vue'

/** The scheduled-tasks tab (plan 191 §0.18, §4.3): one number — how long a
 * run's history is kept — saved through the settings interface. A lone field
 * in a lone group carries no group title. */
describe('CronSettingsTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
      values: { 'cron.retention_days': 30 },
      secrets: {},
      // claudeCodeAvailable: false — a machine without Claude Code, so the
      // peers group stays hidden (plan 195) and this tab keeps its rows.
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    vi.spyOn(forebrainApi, 'saveSettings').mockResolvedValue({ saved: true, applies: 'next_turn' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function mountTab() {
    const wrapper = await mountSettingsTab({ tab: 'cron', component: CronSettingsTab })
    await flushPromises()
    return wrapper
  }

  it('is one plain row: the tab\'s only group carries the field\'s name, not its own title', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('h3').exists()).toBe(false)
    // The prototype's solo group: no group title, the field's own name instead
    // (plan 191 §0.5).
    expect(wrapper.find('[data-field="cron.retention_days"] b').text()).toBe('执行记录保留')
    const box = wrapper.find('[data-num="cron.retention_days"]')
    expect((box.element as HTMLInputElement).value).toBe('30')
    expect(wrapper.find('.settings-num__box em').text()).toBe('天')
    expect(wrapper.find('.settings-num__range').text()).toBe('1–3650')
  })

  it('flags a number past the range the moment it is typed, without a request', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-num="cron.retention_days"]').setValue('4000')
    expect(wrapper.find('.settings-num__box').classes()).toContain('is-bad')
    expect(wrapper.find('[data-field="cron.retention_days"]').find('.settings-row__hint').text()).toBe('范围 1–3650')
    expect(forebrainApi.saveSettings).not.toHaveBeenCalled()
  })

  it('saves the typed number and cancels back to the stored one', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-group="cn"]')
    await wrapper.find('[data-num="cron.retention_days"]').setValue('60')
    await group.find('[data-save="cn"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith({ 'cron.retention_days': 60 }, {})

    await wrapper.find('[data-num="cron.retention_days"]').setValue('90')
    await group.find('.settings-group__cancel').trigger('click')
    expect((wrapper.find('[data-num="cron.retention_days"]').element as HTMLInputElement).value).toBe('60')
    expect(group.find('[data-save="cn"]').exists()).toBe(false)
  })

  it('shows the refusal under the row it names, in the gateway\'s own words', async () => {
    vi.mocked(forebrainApi.saveSettings).mockRejectedValue({
      response: { status: 400, data: { path: 'cron.retention_days', message: '执行记录保留须在 1–3650 天之间' } },
    })
    const wrapper = await mountTab()
    await wrapper.find('[data-num="cron.retention_days"]').setValue('9999')
    await wrapper.find('[data-group="cn"]').find('[data-save="cn"]').trigger('click')
    await flushPromises()

    const row = wrapper.find('[data-field="cron.retention_days"]')
    expect(row.find('.settings-row__error').text()).toBe('执行记录保留须在 1–3650 天之间')
  })

  it('shows the gateway\'s own words when the read fails', async () => {
    vi.mocked(forebrainApi.settings).mockRejectedValue({ response: { status: 500, data: { message: '配置表读不出来' } } })
    const wrapper = await mountTab()
    expect(wrapper.find('[data-tab="cron"]').text()).toContain('配置表读不出来')
  })
})
