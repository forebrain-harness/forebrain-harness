import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount } from '@vue/test-utils'
import { nextTick, ref } from 'vue'

import { forebrainApi, type ActionRecord } from '@/lib/api'
import type { ApprovalDecisionOption } from '@/lib/approvalSuggestions'
import type { ActionRow } from '@/composables/usePendingActions'
import { setLocale } from '@/locales'
import { usePendingActions } from '@/composables/usePendingActions'
import { PendingActionsKey } from '@/composables/providePendingActions'
import ToolGateFooter from './ToolGateFooter.vue'
import tip from '@/directives/tip'

/**
 * The footer that answers a waiting call (plan 138): the refusal follows the
 * engine's decision for the family — cancel opens the words field, where the
 * send names what it will do (written: refuse this one call and carry on;
 * empty, or Esc: refuse and stop), decline acts at once with no field, and an
 * allowing answer folds the field away.
 */
config.global.directives = { tip }

function shellAction(id = 'act-shell'): ActionRow {
  return {
    id,
    kind: 'shell',
    status: 'pending',
    payloadJson: JSON.stringify({ command: 'rm -rf build/', dangerous_rule: 'forced_rm' }),
    createdAt: 1,
    updatedAt: 1,
    permissionSuggestion: {
      permissionToolName: 'Bash',
      permissionInput: 'rm -rf build/',
      availableDecisions: ['accept', 'cancel'],
    },
  } as ActionRecord
}

function permissionsAction(id = 'act-perms'): ActionRow {
  return {
    id,
    kind: 'request_permissions',
    status: 'pending',
    payloadJson: JSON.stringify({ request_permissions: true }),
    createdAt: 1,
    updatedAt: 1,
    permissionSuggestion: {
      permissionToolName: 'Bash',
      permissionInput: 'x',
      availableDecisions: ['grant_for_turn', 'decline'],
    },
  } as ActionRecord
}

/** The footer under the page's own store, with the actions list mocked. */
function mountFooter(record: ActionRow) {
  const store = usePendingActions({ sessionId: ref('s1'), version: ref(0), contextUsedPercent: ref(null) })
  const wrapper = mount(ToolGateFooter, {
    props: {
      action: record,
      options: store.approvalDecisionOptions(record),
      decisionLabel: (option: ApprovalDecisionOption) => store.approvalDecisionLabel(option, record),
      submit: (option: ApprovalDecisionOption) => void store.submitApprovalDecision(record, option),
      refuseWithWords: (a: ActionRow, words: string) => store.refuseWithWords(a, words),
      refuseAndStop: (a: ActionRow) => store.refuseAndStop(a),
      submitting: false,
      error: '',
    },
    global: { provide: { [PendingActionsKey]: store } },
    // Attached, so the field the verdict opens can actually take focus.
    attachTo: document.body,
  })
  return { wrapper, store }
}

describe('ToolGateFooter', () => {
  beforeAll(() => setLocale('zh'))
  afterAll(() => setLocale('en'))

  it('opens the words field on the cancel family\'s refusal, focused', async () => {
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const { wrapper } = mountFooter(shellAction())
    await flushPromises()

    // The refusal keeps its family's own verb, and is a toggle.
    const refusal = wrapper.get('[data-testid="pending-decision-cancel"]')
    expect(refusal.text()).toBe('不要运行')
    expect(refusal.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('[data-testid="gate-feedback"]').exists()).toBe(false)

    await refusal.trigger('click')
    await nextTick()
    expect(refusal.attributes('aria-expanded')).toBe('true')
    const field = wrapper.get('[data-testid="gate-feedback-input"]')
    expect(field.attributes('placeholder')).toBe('告诉它该怎么做（可不填）')
    // The field took focus the moment the verdict opened it.
    expect(field.element).toBe(document.activeElement)

    // Empty send is the refusal that stops the turn (plan 129's cancel).
    expect(wrapper.get('[data-testid="gate-feedback-send"]').text()).toBe('拒绝并停下')
    await wrapper.get('[data-testid="gate-feedback-send"]').trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-shell', { decision: 'cancel', reason: 'web:cancel' })

    // Esc is the same wordless refusal.
    await wrapper.get('[data-testid="gate-feedback-input"]').trigger('keydown', { key: 'Escape' })
    await flushPromises()
    expect(approve).toHaveBeenCalledTimes(2)
    expect(approve).toHaveBeenLastCalledWith('act-shell', { decision: 'cancel', reason: 'web:cancel' })

    // Written words change the send's name and its verdict: this one call is
    // refused, the turn carries on.
    await wrapper.get('[data-testid="gate-feedback-input"]').setValue('改成移到 trash/')
    expect(wrapper.get('[data-testid="gate-feedback-send"]').text()).toBe('发送')
    await wrapper.get('[data-testid="gate-feedback-send"]').trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenLastCalledWith('act-shell', { decision: 'deny', reason: '改成移到 trash/' })
    wrapper.unmount()
  })

  it('declines at once with no words field, the turn carrying on', async () => {
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const { wrapper } = mountFooter(permissionsAction())
    await flushPromises()

    // A decline is a plain button: nothing opens, the decision goes straight
    // out, with the engine's reason format every decision carries.
    expect(wrapper.find('[data-testid="gate-feedback"]').exists()).toBe(false)
    const decline = wrapper.get('[data-testid="pending-decision-decline"]')
    expect(decline.text()).toBe('不授予')
    await decline.trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-perms', expect.objectContaining({
      decision: 'decline', reason: 'web:decline',
    }))
    wrapper.unmount()
  })

  it('folds the words field away on an allowing answer', async () => {
    vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const { wrapper } = mountFooter(shellAction())
    await flushPromises()

    await wrapper.get('[data-testid="pending-decision-cancel"]').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-testid="gate-feedback"]').exists()).toBe(true)
    await wrapper.get('[data-testid="gate-feedback-input"]').setValue('先别动')

    await wrapper.get('[data-testid="pending-decision-accept"]').trigger('click')
    await nextTick()
    expect(wrapper.find('[data-testid="gate-feedback"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
