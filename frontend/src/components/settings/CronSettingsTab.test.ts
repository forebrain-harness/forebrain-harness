import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { forebrainApi, type CronSettings } from '@/lib/api'
import { setLocale } from '@/locales'
import CronSettingsTab from './CronSettingsTab.vue'

const unset: CronSettings = { retentionDays: 30, configured: false, defaultDays: 30, minDays: 1, maxDays: 3650 }

const daysInput = (wrapper: VueWrapper) => wrapper.find('[data-testid="cron-retention-days"]')

async function mountTab(settings: CronSettings) {
  vi.mocked(forebrainApi.cronSettings).mockResolvedValue(settings)
  const wrapper = mount(CronSettingsTab)
  await flushPromises()
  return wrapper
}

describe('CronSettingsTab', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'cronSettings').mockResolvedValue(unset)
    vi.spyOn(forebrainApi, 'saveCronSettings').mockResolvedValue({ applied: true, path: '/tmp/forebrain.yaml' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('shows the value in force, with the default hint when none is set', async () => {
    const wrapper = await mountTab(unset)
    expect((daysInput(wrapper).element as HTMLInputElement).value).toBe('30')
    expect(wrapper.text()).toContain('30 days when not set.')
  })

  it('refuses an out-of-range number in the viewer\'s language, without a request', async () => {
    const wrapper = await mountTab(unset)
    await daysInput(wrapper).setValue('0')
    const error = wrapper.find('[data-testid="cron-retention-error"]')
    expect(error.text()).toBe('Enter a whole number from 1 to 3650.')
    await wrapper.find('[data-testid="cron-retention-save"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveCronSettings).not.toHaveBeenCalled()
  })

  it('saves the typed number', async () => {
    const wrapper = await mountTab(unset)
    await daysInput(wrapper).setValue('7')
    await wrapper.find('[data-testid="cron-retention-save"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveCronSettings).toHaveBeenCalledWith(7)
    expect(wrapper.text()).toContain('Saved; it applies from the next cleanup.')
  })

  it('restoring the default removes the key and shows the default', async () => {
    vi.mocked(forebrainApi.cronSettings)
      .mockResolvedValueOnce({ ...unset, retentionDays: 7, configured: true })
      .mockResolvedValueOnce(unset)
    const wrapper = await mountTab(unset)
    expect((daysInput(wrapper).element as HTMLInputElement).value).toBe('7')

    await wrapper.find('[data-testid="cron-retention-reset"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveCronSettings).toHaveBeenCalledWith(null)
    expect((daysInput(wrapper).element as HTMLInputElement).value).toBe('30')
    expect(wrapper.text()).toContain('30 days when not set.')
  })
})
