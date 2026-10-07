import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ScheduleBuilder from './ScheduleBuilder.vue'
import forebrainApi from '@/lib/api'

describe('ScheduleBuilder', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('opens a stored schedule as-is instead of rebuilding it from defaults', async () => {
    vi.spyOn(forebrainApi, 'cronPreview').mockResolvedValue({ raw: 'daily at 7am', valid: true, kind: 'cron', next: [] })
    const wrapper = mount(ScheduleBuilder, { props: { modelValue: 'daily at 7am' } })
    // Editing a job must not change its schedule before the user does.
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toBe('daily at 7am')
    expect((wrapper.find('[data-testid="schedule-mode"]').element as HTMLSelectElement).value).toBe('cron')
    expect((wrapper.find('[data-testid="schedule-cron"]').element as HTMLInputElement).value).toBe('daily at 7am')
  })

  it('starts a new schedule from the interval mode', () => {
    vi.spyOn(forebrainApi, 'cronPreview').mockResolvedValue({ raw: 'every 1h', valid: true, kind: 'every', next: [] })
    const wrapper = mount(ScheduleBuilder, { props: { modelValue: '' } })
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toBe('every 1h')
  })

  it('ignores a preview answer for an expression the user has since changed', async () => {
    vi.useFakeTimers()
    let resolveFirst: (value: { raw: string; valid: boolean }) => void = () => {}
    const preview = vi.spyOn(forebrainApi, 'cronPreview')
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValueOnce({ raw: 'not a schedule', valid: false, error: 'unrecognized schedule' })
    const wrapper = mount(ScheduleBuilder, { props: { modelValue: 'every 5m' } })
    await vi.advanceTimersByTimeAsync(400)
    expect(preview).toHaveBeenCalledTimes(1)

    await wrapper.find('[data-testid="schedule-cron"]').setValue('not a schedule')
    await vi.advanceTimersByTimeAsync(400)
    await flushPromises()
    // The first request answers last; its verdict is about "every 5m".
    resolveFirst({ raw: 'every 5m', valid: true })
    await flushPromises()

    expect(wrapper.emitted('validated')?.slice(-1)[0]?.[0]).toBe(false)
  })
})
