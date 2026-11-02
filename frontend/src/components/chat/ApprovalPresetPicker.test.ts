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

  // The shared composer control both the chat page and the skill workshop
  // render. On a gateway with no sandbox there is one posture and no choice:
  // the switch stays on screen — a person must be able to see that nothing is
  // being asked — fixed at full access and disabled (owner 2026-10-31, plan 215
  // §3.4).
  it('is fixed at full access and disabled when there is no sandbox', async () => {
    vi.spyOn(forebrainApi, 'approvalPresetsVisible').mockResolvedValue(false)
    vi.spyOn(forebrainApi, 'sessionPresetCurrent').mockResolvedValue({ current: 'full_access' })
    const session = vi.spyOn(forebrainApi, 'sessionPreset').mockResolvedValue({ ok: true })
    const wrapper = mount(ApprovalPresetPicker, { props: { sessionId: 's1' } })
    const button = wrapper.get('[data-testid="approval-picker"]')
    await vi.waitFor(() => expect(button.text()).toBe('Full Access'))
    expect(button.attributes('disabled')).toBeDefined()

    // No menu and no second option: there is nothing to pick.
    await button.trigger('click')
    expect(wrapper.findAll('[role="option"]')).toHaveLength(0)
    expect(session).not.toHaveBeenCalled()
    // And no hint of a sandbox, in the words the plan keeps off the screen.
    expect(wrapper.text()).not.toContain('YOLO')
    expect(wrapper.text()).not.toContain('Sandbox unavailable')
  })

  it('keeps both presets when the machine has a sandbox', async () => {
    vi.spyOn(forebrainApi, 'approvalPresetsVisible').mockResolvedValue(true)
    const wrapper = mountPicker('workspace_write')
    const button = wrapper.get('[data-testid="approval-picker"]')
    await vi.waitFor(() => expect(button.text()).toBe('Default'))
    expect(button.attributes('disabled')).toBeUndefined()
    await button.trigger('click')
    expect(wrapper.findAll('[role="option"]')).toHaveLength(2)
  })

  it('names the tier alone, without an approvals prefix', async () => {
    const wrapper = mountPicker('workspace_write')
    await vi.waitFor(() => expect(wrapper.get('[data-testid="approval-picker"]').text()).toBe('Default'))
    expect(wrapper.get('[data-testid="approval-picker"]').text()).not.toContain('Approvals:')
  })

  it('keeps the session-scoped switcher behind the button', async () => {
    const wrapper = mountPicker('workspace_write')
    await vi.waitFor(() => expect(wrapper.get('[data-testid="approval-picker"]').text()).toBe('Default'))
    // The tip still says what the button is, now that the label cannot.
    expect(wrapper.get('[data-testid="approval-picker"]').attributes('aria-label')).toBe('Approval mode for this session')

    await wrapper.get('[data-testid="approval-picker"]').trigger('click')
    expect(wrapper.find('[data-testid="approval-option-full_access"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="approval-option-workspace_write"]').attributes('aria-selected')).toBe('true')
    // Two options and no read-only one (plan 203): the terminal's /permissions
    // offers the same two.
    const options = wrapper.findAll('[role="option"]')
    expect(options).toHaveLength(2)
    expect(options.map((option) => option.attributes('data-testid'))).toEqual([
      'approval-option-workspace_write',
      'approval-option-full_access',
    ])
  })

  // A new chat has no conversation, and the switch still works: it shows the
  // agent's default, and a pick is held until the page hands it to the
  // conversation the first message opens.
  describe('on a new chat (no session yet)', () => {
    it('is usable, shows the agent default, and holds a pick as a draft', async () => {
      vi.spyOn(forebrainApi, 'approvalDefault').mockResolvedValue({ current: 'workspace_write' })
      const session = vi.spyOn(forebrainApi, 'sessionPreset').mockResolvedValue({ ok: true })
      const wrapper = mount(ApprovalPresetPicker, { props: { sessionId: null } })
      await vi.waitFor(() => expect(wrapper.get('[data-testid="approval-picker"]').text()).toBe('Default'))
      expect(wrapper.get('[data-testid="approval-picker"]').attributes('disabled')).toBeUndefined()

      await wrapper.get('[data-testid="approval-picker"]').trigger('click')
      await wrapper.get('[data-testid="approval-option-full_access"]').trigger('click')
      // The label follows the pick, and nothing was written: there is no session to write to.
      expect(wrapper.get('[data-testid="approval-picker"]').text()).toBe('Full Access')
      expect(session).not.toHaveBeenCalled()

      const exposed = wrapper.vm as unknown as { takeDraft: () => string | null }
      expect(exposed.takeDraft()).toBe('full_access')
      // Handed over once.
      expect(exposed.takeDraft()).toBeNull()
    })

    it('has no draft when nothing was picked', async () => {
      vi.spyOn(forebrainApi, 'approvalDefault').mockResolvedValue({ current: 'workspace_write' })
      const wrapper = mount(ApprovalPresetPicker, { props: { sessionId: null } })
      await vi.waitFor(() => expect(wrapper.get('[data-testid="approval-picker"]').text()).toBe('Default'))
      expect((wrapper.vm as unknown as { takeDraft: () => string | null }).takeDraft()).toBeNull()
    })
  })
})
