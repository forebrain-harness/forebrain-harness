import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import ScheduleBuilder from './ScheduleBuilder.vue'
import forebrainApi from '@/lib/api'
import { setLocale } from '@/locales'
import { pickOption, selectedText } from '@/test-utils/formControls'

/** The builder the job editor draws: one mode, its own fields, one preview. */
function mountBuilder(modelValue = ''): ReturnType<typeof mount> {
  return mount(ScheduleBuilder, {
    props: { modelValue, projectId: 'p1' },
    // The mode and unit selects are the library's, whose items render through a
    // portal: attached to the document, the shared `pickOption` reaches them the
    // way a person's clicks would.
    attachTo: document.body,
  })
}

/** The mode select's trigger — the button the select is opened on. */
function modeTrigger(wrapper: ReturnType<typeof mountBuilder>): HTMLElement {
  return wrapper.find('[data-testid="schedule-mode"]').element as HTMLElement
}

// Each test mounts a builder on the document; one left behind would keep its
// options there for the next test's document-wide lookups.
enableAutoUnmount(afterEach)

describe('ScheduleBuilder', () => {
  beforeAll(() => {
    setLocale('zh')
    // The selects measure their viewport with ResizeObserver, which jsdom does
    // not have; scrolling an item into view is not implemented either.
    ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    Element.prototype.scrollIntoView = () => undefined
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('opens a stored schedule as-is instead of rebuilding it from defaults', async () => {
    vi.spyOn(forebrainApi, 'cronPreview').mockResolvedValue({ raw: 'daily at 7am', valid: true, kind: 'cron', next: [] })
    const wrapper = mountBuilder('daily at 7am')
    // Editing a job must not change its schedule before the user does.
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toBe('daily at 7am')
    await flushPromises()
    expect(selectedText(modeTrigger(wrapper))).toBe('Cron 表达式')
    expect((wrapper.find('[data-testid="schedule-cron"]').element as HTMLInputElement).value).toBe('daily at 7am')
  })

  it('starts a new schedule from the interval mode', () => {
    vi.spyOn(forebrainApi, 'cronPreview').mockResolvedValue({ raw: 'every 1h', valid: true, kind: 'every', next: [] })
    const wrapper = mountBuilder()
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toBe('every 1h')
  })

  it('ignores a preview answer for an expression the user has since changed', async () => {
    vi.useFakeTimers()
    let resolveFirst: (value: { raw: string; valid: boolean }) => void = () => {}
    const preview = vi.spyOn(forebrainApi, 'cronPreview')
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValueOnce({ raw: 'not a schedule', valid: false, error: 'unrecognized schedule' })
    const wrapper = mountBuilder('every 5m')
    await vi.advanceTimersByTimeAsync(400)
    expect(preview).toHaveBeenCalledTimes(1)

    await wrapper.find('[data-testid="schedule-cron"]').setValue('not a schedule')
    // The library's Input hands its value on from a watcher, so the model update
    // — and the debounce timer it schedules — is a flush behind the DOM event
    // rather than part of it. Settle that flush before moving the clock, so the
    // timer exists when the clock does.
    await flushPromises()
    await vi.advanceTimersByTimeAsync(400)
    await flushPromises()
    // The first request answers last; its verdict is about "every 5m".
    resolveFirst({ raw: 'every 5m', valid: true })
    await flushPromises()

    expect(wrapper.emitted('validated')?.slice(-1)[0]?.[0]).toBe(false)
  })

  it('keeps the two fields browsers render as pickers', async () => {
    const wrapper = mountBuilder()

    // Nothing else looks at these two: a datetime-local or a time input that
    // lost its `type` is a free-text box, and no other test would say so.
    await pickOption(modeTrigger(wrapper), '一次 · 指定时间')
    expect(wrapper.find('[data-testid="schedule-once-at"]').attributes('type')).toBe('datetime-local')

    await pickOption(modeTrigger(wrapper), '每天')
    expect(wrapper.find('[data-testid="schedule-time"]').attributes('type')).toBe('time')
  })

  it('holds a typed count as a number, and an emptied one at the floor', async () => {
    vi.spyOn(forebrainApi, 'cronPreview').mockResolvedValue({ raw: 'in 3m', valid: true, kind: 'once-in', next: [] })
    const wrapper = mountBuilder()
    await pickOption(modeTrigger(wrapper), '一次 · 多久之后')

    const count = wrapper.find('[data-testid="schedule-in-count"]')
    await count.setValue('3')
    // A count the field held as the string '3' would fail the builder's own
    // `Number.isFinite` and leave no expression at all.
    expect(wrapper.emitted('update:modelValue')?.slice(-1)[0]?.[0]).toBe('in 3m')

    await count.setValue('')
    expect(wrapper.emitted('update:modelValue')?.slice(-1)[0]?.[0]).toBe('in 1m')
  })
})
