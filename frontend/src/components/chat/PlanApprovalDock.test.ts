import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, flushPromises, mount } from '@vue/test-utils'
import { nextTick, ref } from 'vue'

import { forebrainApi, type ActionRecord, type SessionApprovalRequest } from '@/lib/api'
import { setLocale } from '@/locales'
import { usePendingActions } from '@/composables/usePendingActions'
import { PendingActionsKey } from '@/composables/providePendingActions'
import type { SessionPlan } from '@/composables/useSessionPlans'
import type { ActionRow } from '@/composables/usePendingActions'
import PlanApprovalDock from './PlanApprovalDock.vue'
import tip from '@/directives/tip'

/**
 * The plan approval where the input was (plan 138): the plan and its verdicts
 * replace the composer while the planner waits — the plan itself scrolled and
 * fading, the four verdicts on the tinted footer every approval closes on,
 * keep planning's words required (an empty send stays gray, Esc is the
 * wordless refusal), a review in flight one line and its stop, and the eye
 * opening the whole plan in the drawer's plan tab.
 */
config.global.directives = { tip }

const PLAN_TEXT = '# 计划：退出计划模式的网页审批\n\n1. 先读 README。\n2. 再对照计划逐条检查。\n'

function exitPlanAction(id = 'act-exit'): ActionRow {
  return {
    id,
    kind: 'exit_plan_mode',
    status: 'pending',
    payloadJson: JSON.stringify({ session_id: 's1' }),
    createdAt: 1,
    updatedAt: 1,
  } as ActionRecord
}

/** The dock under the page's own store, with its reads mocked. */
function mountDock(
  record: ActionRow,
  request: Partial<SessionApprovalRequest> = {},
  onOpenPlan?: (plan: SessionPlan) => void,
) {
  vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([record as ActionRecord])
  vi.spyOn(forebrainApi, 'sessionApprovalRequest').mockResolvedValue({
    actionId: record.id,
    kind: 'exit_plan_mode',
    planText: PLAN_TEXT,
    planReviewModels: [],
    planReviews: [],
    planReviewActive: null,
    ...request,
  } as SessionApprovalRequest)
  vi.spyOn(forebrainApi, 'sessionPlans').mockResolvedValue([
    { name: 'plan.md', path: '/w/plans/138.md', updatedAt: 2, markdown: PLAN_TEXT },
  ])
  const store = usePendingActions({ sessionId: ref('s1'), version: ref(0), contextUsedPercent: ref(72) })
  const wrapper = mount(PlanApprovalDock, {
    props: {
      action: record,
      sessionId: 's1',
      ...(onOpenPlan ? { onOpenPlan } : {}),
    },
    global: { provide: { [PendingActionsKey]: store } },
    attachTo: document.body,
  })
  return { wrapper, store }
}

describe('PlanApprovalDock', () => {
  beforeAll(() => setLocale('zh'))
  afterAll(() => setLocale('en'))

  it('shows the plan and its choices where the input was', async () => {
    const { wrapper } = mountDock(exitPlanAction())
    await flushPromises()

    const dock = wrapper.get('[data-testid="exit-plan-approval"]')
    // The head names the plan and its file; the body is the plan itself.
    expect(dock.text()).toContain('计划')
    expect(dock.text()).toContain('/w/plans/138.md')
    const planBody = wrapper.get('[data-testid="exit-plan-text"]')
    await vi.waitFor(() => {
      expect(planBody.text()).toContain('再对照计划逐条检查')
    })
    // The four choices in the overlay's order, the clear-context one naming
    // what it frees; the feedback field waits for its own verdict.
    expect(wrapper.get('[data-testid="exit-plan-approve-clear"]').text()).toBe('批准并清空上下文（已用 72%）')
    expect(wrapper.get('[data-testid="exit-plan-approve"]').text()).toBe('批准')
    expect(wrapper.get('[data-testid="exit-plan-keep-planning"]').text()).toBe('继续规划')
    expect(wrapper.find('[data-testid="exit-plan-ask-review"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="exit-plan-feedback"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps keep-planning\'s send gray until there are words; Esc refuses and stops', async () => {
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const deny = vi.spyOn(forebrainApi, 'actionsDeny').mockResolvedValue({} as never)
    const { wrapper } = mountDock(exitPlanAction())
    await flushPromises()

    // The verdict opens the field; an empty send is disabled — the words are
    // required, they are what the planner reads.
    await wrapper.get('[data-testid="exit-plan-keep-planning"]').trigger('click')
    await nextTick()
    const send = wrapper.get('[data-testid="exit-plan-send-feedback"]')
    expect(send.attributes('disabled')).toBeDefined()

    await wrapper.get('[data-testid="exit-plan-feedback-input"]').setValue('把第二步改成先写测试')
    expect(send.attributes('disabled')).toBeUndefined()

    // Enter sends the words to the planner (a deny with the user's reason).
    await wrapper.get('[data-testid="exit-plan-feedback-input"]').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(deny).toHaveBeenCalledWith('act-exit', { reason: '把第二步改成先写测试' })

    // Esc on the words is the wordless refusal: the stop, not the collapse.
    await wrapper.get('[data-testid="exit-plan-keep-planning"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="exit-plan-feedback-input"]').trigger('keydown', { key: 'Escape' })
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-exit', { decision: 'cancel', reason: 'web:cancel' })
    wrapper.unmount()
  })

  it('opens the whole plan in the drawer from the eye', async () => {
    const opened: SessionPlan[] = []
    const { wrapper } = mountDock(exitPlanAction(), {}, (plan) => opened.push(plan))
    await flushPromises()

    await wrapper.get('[data-testid="exit-plan-open-plan"]').trigger('click')
    expect(opened).toHaveLength(1)
    expect(opened[0].path).toBe('/w/plans/138.md')
    wrapper.unmount()
  })

  it('says a review in flight and offers its stop; a picker lists the models', async () => {
    const running = mountDock(exitPlanAction('act-exit'), {
      planReviewActive: { reviewId: 'r1', provider: 'deepseek', model: 'deepseek-v4-flash', agentId: 'agent-r1' },
    })
    await flushPromises()
    expect(running.wrapper.get('[data-testid="exit-plan-approval"]').text()).toContain('正在评审这份计划')
    expect(running.wrapper.get('[data-testid="exit-plan-stop-review"]').text()).toBe('停止')
    running.wrapper.unmount()

    // With reviewers configured, the ask-review verdict is there and its list
    // opens on the footer, the current one marked.
    const withModels = mountDock(exitPlanAction('act-exit'), {
      planReviewModels: [
        { provider: 'deepseek', model: 'deepseek-v4-flash', current: true },
        { provider: 'zhipuai', model: 'glm-5.3-flash' },
      ],
    })
    await flushPromises()
    await withModels.wrapper.get('[data-testid="exit-plan-ask-review"]').trigger('click')
    await nextTick()
    const models = withModels.wrapper.findAll('[data-testid="plan-review-model"]')
    expect(models.map((m) => m.text())).toEqual(['deepseek/deepseek-v4-flash当前', 'zhipuai/glm-5.3-flash'])
    withModels.wrapper.unmount()
  })
})
