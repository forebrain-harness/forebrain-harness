import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import ApprovalPresetPicker from './ApprovalPresetPicker.vue'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

/**
 * The composer's approval button names the tier alone: the chip sits among
 * controls whose meaning the tip already explains, so no "Approvals:" prefix
 * prefixes the label (owner 2026-10-18).
 */
describe('ApprovalPresetPicker', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    vi.restoreAllMocks()
    setLocale('en')
  })

  function mountPicker(current: string | null = null) {
    vi.spyOn(forebrainApi, 'sessionPresetCurrent').mockResolvedValue({ current })
    return mount(ApprovalPresetPicker, { props: { sessionId: 's1' } })
  }

  it('names the tier alone, without an approvals prefix', async () => {
    const wrapper = mountPicker('auto')
    await vi.waitFor(() => expect(wrapper.get('[data-testid="approval-picker"]').text()).toBe('Default'))
    expect(wrapper.get('[data-testid="approval-picker"]').text()).not.toContain('Approvals:')
  })

  it('keeps the session-scoped switcher behind the button', async () => {
    const wrapper = mountPicker('read-only')
    await vi.waitFor(() => expect(wrapper.get('[data-testid="approval-picker"]').text()).toBe('Read Only'))
    // The tip still says what the button is, now that the label cannot.
    expect(wrapper.get('[data-testid="approval-picker"]').attributes('aria-label')).toBe('Approval mode for this session')

    await wrapper.get('[data-testid="approval-picker"]').trigger('click')
    expect(wrapper.find('[data-testid="approval-option-full-access"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="approval-option-read-only"]').attributes('aria-selected')).toBe('true')
  })
})
