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
  extra: { sessionType?: string } = {},
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
      ...(extra.sessionType ? { sessionType: extra.sessionType } : {}),
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

  // plan 283: handing the plan to another model is a verdict of its own, and
  // "review it again" continues the reviewer the user already asked. Both rows
  // appear only when the engine says they can, and the picker's caption says
  // which of the two lists is open.
  it('offers the hand-off and the re-review only when the gate allows them', async () => {
    const plain = mountDock(exitPlanAction(), {}, undefined, {})
    await flushPromises()
    expect(plain.wrapper.find('[data-testid="exit-plan-hand-off"]').exists()).toBe(false)
    expect(plain.wrapper.find('[data-testid="exit-plan-review-again"]').exists()).toBe(false)
    plain.wrapper.unmount()

    const allowed = mountDock(exitPlanAction(), {
      handOffAvailable: true,
      planReviewModels: [
        { id: 'row-a', provider: 'deepseek', model: 'deepseek-v4-flash', current: true },
        { id: 'row-b', provider: 'zhipuai', model: 'glm-5.3' },
      ],
      previousPlanReview: {
        reviewId: 'plan-review:1',
        agentId: 'agent-r1',
        model: { id: 'row-b', provider: 'zhipuai', model: 'glm-5.3' },
      },
    })
    await flushPromises()
    const acts = allowed.wrapper.findAll('.gate-acts .gate-btn').map((b) => b.text())
    expect(acts).toEqual([
      '批准并清空上下文（已用 72%）',
      '批准',
      '交给其他模型执行',
      '请其他模型评审',
      '请 zhipuai/glm-5.3 再评审',
      '继续规划',
    ])
    allowed.wrapper.unmount()
  })

  it('hands the plan to the row the user picks', async () => {
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const { wrapper } = mountDock(exitPlanAction(), {
      handOffAvailable: true,
      planReviewModels: [
        { id: 'row-a', provider: 'deepseek', model: 'deepseek-v4-flash', current: true },
        { id: 'row-b', provider: 'zhipuai', model: 'glm-5.3' },
      ],
    })
    await flushPromises()
    await wrapper.get('[data-testid="exit-plan-hand-off"]').trigger('click')
    await nextTick()
    expect(wrapper.get('.xp-pick .cap').text()).toBe('交给哪个模型执行？')
    expect(wrapper.get('.xp-pick .hint').text()).toBe('在新的 worktree 里实施，不动当前分支')
    const rows = wrapper.findAll('[data-testid="exit-plan-hand-off-model"]')
    expect(rows).toHaveLength(2)
    await rows[1].trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-exit', {
      handOffModel: { id: 'row-b', provider: 'zhipuai', model: 'glm-5.3' },
    })
    wrapper.unmount()
  })

  // plan 289: Esc folds the picker away instead of re-entering
  // toggleModelPicker with the default 'review', which would quietly turn a
  // hand-off into a review. The "no request went out" assertion is what pins
  // the purpose down — seeing .xp-pick disappear is not enough.
  it('Esc closes the hand-off picker without turning it into a review', async () => {
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const review = vi.spyOn(forebrainApi, 'actionPlanReview').mockResolvedValue(true)
    // This file has no restoreAllMocks between cases: clear the shared spies'
    // history so "no request went out" is about this case alone.
    approve.mockClear()
    review.mockClear()
    const { wrapper, store } = mountDock(exitPlanAction(), {
      handOffAvailable: true,
      planReviewModels: [
        { id: 'row-a', provider: 'deepseek', model: 'deepseek-v4-flash', current: true },
        { id: 'row-b', provider: 'zhipuai', model: 'glm-5.3' },
      ],
    })
    await flushPromises()

    await wrapper.get('[data-testid="exit-plan-hand-off"]').trigger('click')
    await nextTick()
    expect(wrapper.get('.xp-pick .cap').text()).toBe('交给哪个模型执行？')
    expect(wrapper.findAll('[data-testid="exit-plan-hand-off-model"]')).toHaveLength(2)

    await wrapper.get('[data-testid="exit-plan-approval"]').trigger('keydown', { key: 'Escape' })
    await nextTick()
    expect(wrapper.findAll('.xp-pick')).toHaveLength(0)
    expect(store.modelPicking.value['act-exit']).toBe(false)

    // Before the user picks again: neither a review nor an approval was sent.
    expect(review).not.toHaveBeenCalled()
    expect(approve).not.toHaveBeenCalled()

    // Re-opening the same list still hands off, and the row reaches
    // actionsApprove — Esc changed neither the purpose nor the verdict.
    await wrapper.get('[data-testid="exit-plan-hand-off"]').trigger('click')
    await nextTick()
    expect(wrapper.get('.xp-pick .cap').text()).toBe('交给哪个模型执行？')
    const rows = wrapper.findAll('[data-testid="exit-plan-hand-off-model"]')
    expect(rows).toHaveLength(2)
    await rows[1].trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-exit', {
      handOffModel: { id: 'row-b', provider: 'zhipuai', model: 'glm-5.3' },
    })
    expect(review).not.toHaveBeenCalled()
    wrapper.unmount()

    // Symmetry: closeModelPicker is purpose-agnostic, so Esc also closes the
    // reviewer's list opened with 'review'.
    const reviewDock = mountDock(exitPlanAction(), {
      planReviewModels: [
        { provider: 'deepseek', model: 'deepseek-v4-flash', current: true },
        { provider: 'zhipuai', model: 'glm-5.3-flash' },
      ],
    })
    await flushPromises()
    await reviewDock.wrapper.get('[data-testid="exit-plan-ask-review"]').trigger('click')
    await nextTick()
    expect(reviewDock.wrapper.findAll('[data-testid="plan-review-model"]')).toHaveLength(2)
    await reviewDock.wrapper.get('[data-testid="exit-plan-approval"]').trigger('keydown', { key: 'Escape' })
    await nextTick()
    expect(reviewDock.wrapper.findAll('.xp-pick')).toHaveLength(0)
    expect(reviewDock.store.modelPicking.value['act-exit']).toBe(false)
    reviewDock.wrapper.unmount()
  })

  it('re-reviews through the previous reviewer without opening the list', async () => {
    const review = vi.spyOn(forebrainApi, 'actionPlanReview').mockResolvedValue(true)
    const { wrapper } = mountDock(exitPlanAction(), {
      previousPlanReview: {
        reviewId: 'plan-review:1',
        agentId: 'agent-r1',
        model: { id: 'row-b', provider: 'zhipuai', model: 'glm-5.3' },
      },
    })
    await flushPromises()
    await wrapper.get('[data-testid="exit-plan-review-again"]').trigger('click')
    await flushPromises()
    expect(review).toHaveBeenCalledWith('act-exit', { again: true })
    expect(wrapper.find('.xp-pick').exists()).toBe(false)
    wrapper.unmount()
  })

  // plan 153 A9: an office conversation answers its plan in the office's own
  // four words. "Running it" is the plain approval, "revising it" is the same
  // keep-planning refusal coding uses (words required), and "keep the plan
  // only" grants the approval with plan_only — the plan becomes this turn's
  // deliverable and the work does not start.
  it('answers an office plan with its own four verdicts', async () => {
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const { wrapper } = mountDock(exitPlanAction(), {}, undefined, { sessionType: 'office' })
    await flushPromises()

    expect(wrapper.get('.gate-ask .q').text()).toBe('计划写好了，怎么处理？')
    expect(wrapper.get('[data-testid="office-plan-execute"]').text()).toBe('按计划执行')
    expect(wrapper.get('[data-testid="office-plan-revise"]').text()).toBe('修改计划')
    expect(wrapper.get('[data-testid="office-plan-keep-only"]').text()).toBe('只要计划，先不执行')
    // No clear-context verdict and no coding wording: the office dock is its
    // own four, not the coding four with new labels.
    expect(wrapper.find('[data-testid="exit-plan-approve-clear"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="exit-plan-approve"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="exit-plan-keep-planning"]').exists()).toBe(false)

    await wrapper.get('[data-testid="office-plan-keep-only"]').trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-exit', { planOnly: true })
    wrapper.unmount()
  })

  it('revises an office plan through the required words, and runs it plainly', async () => {
    const approve = vi.spyOn(forebrainApi, 'actionsApprove').mockResolvedValue({} as never)
    const deny = vi.spyOn(forebrainApi, 'actionsDeny').mockResolvedValue({} as never)
    const { wrapper } = mountDock(exitPlanAction(), {}, undefined, { sessionType: 'office' })
    await flushPromises()

    await wrapper.get('[data-testid="office-plan-revise"]').trigger('click')
    await nextTick()
    const send = wrapper.get('[data-testid="exit-plan-send-feedback"]')
    expect(send.attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="exit-plan-feedback-input"]').setValue('分工那一节加上存货监盘')
    await wrapper.get('[data-testid="exit-plan-send-feedback"]').trigger('click')
    await flushPromises()
    expect(deny).toHaveBeenCalledWith('act-exit', { reason: '分工那一节加上存货监盘' })

    // "Run the plan" is the approval without either coding extra.
    await wrapper.get('[data-testid="office-plan-execute"]').trigger('click')
    await flushPromises()
    expect(approve).toHaveBeenCalledWith('act-exit', {})
    wrapper.unmount()
  })

  it('leaves a coding plan on the four verdicts it always had', async () => {
    const { wrapper } = mountDock(exitPlanAction(), {}, undefined, { sessionType: 'coding' })
    await flushPromises()

    expect(wrapper.get('.gate-ask .q').text()).toBe('要开始执行吗？')
    expect(wrapper.get('[data-testid="exit-plan-approve-clear"]').text()).toBe('批准并清空上下文（已用 72%）')
    expect(wrapper.get('[data-testid="exit-plan-approve"]').text()).toBe('批准')
    expect(wrapper.get('[data-testid="exit-plan-keep-planning"]').text()).toBe('继续规划')
    expect(wrapper.find('[data-testid="office-plan-execute"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="office-plan-keep-only"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
