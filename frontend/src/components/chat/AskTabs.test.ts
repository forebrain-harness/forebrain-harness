import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'

import { forebrainApi, type ActionRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import AskTabs from './AskTabs.vue'
import { usePendingActions, type AskAction } from '@/composables/usePendingActions'

/**
 * The agent's questions, asked the way the terminal's overlay asks them: one
 * question per screen, a ☐/✔ tab per question, a review before submit. These
 * tests pin that shape — the tab bar's rules (which forms get arrows and a
 * submit tab), the numbered options, the preview, the「其他」field — and, with
 * the real composable behind the card, the exact answers payload the engine
 * receives, unchanged from the flat form it replaces.
 */

type Question = AskAction['form']['questions'][number]

function question(overrides: Partial<Question> & Pick<Question, 'id' | 'prompt'>): Question {
  return {
    options: [
      { id: `${overrides.id}-a`, label: 'Option A', description: 'The first choice' },
      { id: `${overrides.id}-b`, label: 'Option B', description: 'The second choice' },
    ],
    allowMultiple: false,
    allowOther: true,
    ...overrides,
  }
}

function mountAsk(questions: Question[], state: { answers?: Record<string, string | string[]>; others?: Record<string, string> } = {}) {
  // The store the parents keep: one record per action, mutated in place — the
  // card reads the same object through its props, exactly as the composable's
  // setAskAnswer writes it.
  const answers = ref(state.answers ?? {})
  const others = ref(state.others ?? {})
  const submitted: unknown[] = []
  const wrapper = mount(AskTabs, {
    props: {
      questions,
      answers: answers.value,
      others: others.value,
      submitting: false,
      onSetAnswer: (questionId: string, value: string | string[]) => {
        answers.value[questionId] = value
      },
      onSetOther: (questionId: string, value: string) => {
        others.value[questionId] = value
      },
      onSubmit: () => {
        submitted.push({
          answers: questions.map((q) => {
            const raw = answers.value[q.id]
            const optionIds = Array.isArray(raw) ? raw : (raw ? [String(raw)] : [])
            const otherText = String(others.value[q.id] ?? '').trim()
            return { questionId: q.id, optionIds, ...(otherText ? { otherText } : {}) }
          }),
        })
      },
    },
  })
  return { wrapper, answers, others, submitted }
}

describe('AskTabs', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => vi.restoreAllMocks())

  it('asks one single-select question without arrows or a submit tab, and a pick submits it', async () => {
    const { wrapper, submitted } = mountAsk([question({ id: '范围', prompt: '要动哪些范围？', header: '范围' })])

    // A single single-select question needs no way forward: no arrows, no
    // submit chip — the one chip is the question itself.
    const nav = wrapper.find('[data-testid="ask-tabs-nav"]')
    expect(nav.text()).toContain('☐ 范围')
    expect(nav.text()).not.toContain('→')
    expect(wrapper.find('[data-testid="ask-tab-submit"]').exists()).toBe(false)

    const rows = wrapper.findAll('[data-testid="ask-option"]')
    expect(rows[0].text()).toContain('1.')
    expect(rows[0].text()).toContain('Option A')
    expect(rows[1].text()).toContain('2.')
    expect(rows[1].text()).toContain('Option B')
    await rows[1].trigger('click')

    const events = wrapper.emitted('set-answer')
    expect(events).toEqual([['范围', '范围-b']])
    // The pick is the answer and the submission, the overlay's Enter.
    expect(wrapper.emitted('submit')).toHaveLength(1)
    expect(submitted).toEqual([{ answers: [{ questionId: '范围', optionIds: ['范围-b'] }] }])
  })

  it('toggles a multi-select question without moving on, then reviews before submit', async () => {
    const { wrapper } = mountAsk([
      question({ id: '补充', prompt: '还要补充什么？', header: '补充', allowMultiple: true }),
    ])

    const rows = wrapper.findAll('[data-testid="ask-option"]')
    await rows[0].trigger('click')
    await rows[1].trigger('click')
    // A multi-select toggle never advances and never submits.
    expect(wrapper.emitted('submit')).toBeUndefined()
    expect(wrapper.emitted('set-answer')).toEqual([
      ['补充', ['补充-a']],
      ['补充', ['补充-a', '补充-b']],
    ])
    // Toggling back off removes the pick.
    await rows[0].trigger('click')
    const setAnswerEvents = wrapper.emitted('set-answer') ?? []
    expect(setAnswerEvents[setAnswerEvents.length - 1]).toEqual(['补充', ['补充-b']])

    // The submit tab is there (a multi-select form has one) and behind it the
    // recap of what was picked.
    expect(wrapper.find('[data-testid="ask-tab-submit"]').exists()).toBe(true)
    await wrapper.find('[data-testid="ask-tab-submit"]').trigger('click')
    const review = wrapper.find('[data-testid="ask-review"]')
    expect(review.exists()).toBe(true)
    expect(review.text()).toContain('还要补充什么？')
    expect(review.text()).toContain('→ Option B')
    await review.find('[data-testid="ask-submit"]').trigger('click')
    expect(wrapper.emitted('submit')).toHaveLength(1)
  })

  it('answers a multi-question form one screen at a time, flipping its tab to ✔', async () => {
    const { wrapper } = mountAsk([
      question({ id: '范围', prompt: '要动哪些范围？', header: '范围' }),
      question({ id: '交付', prompt: '这次怎么交付？', header: '交付' }),
    ])

    // The answered tab is ✔ while the next is ☐ and on screen.
    await wrapper.findAll('[data-testid="ask-option"]')[0].trigger('click')
    expect(wrapper.find('[data-testid="ask-tab-0"]').attributes('data-answered')).toBe('true')
    expect(wrapper.find('[data-testid="ask-tab-1"]').attributes('data-answered')).toBe('false')
    expect(wrapper.text()).toContain('这次怎么交付？')

    // A tab goes back to its own question.
    await wrapper.find('[data-testid="ask-tab-0"]').trigger('click')
    expect(wrapper.text()).toContain('要动哪些范围？')
  })

  it('expands the chosen option\'s preview under it, and only that one', async () => {
    const { wrapper } = mountAsk([
      question({
        id: '方案', prompt: '选哪个方案？', header: '方案',
        options: [
          { id: '方案-a', label: 'Option A', preview: 'option A preview\nsecond line' },
          { id: '方案-b', label: 'Option B', preview: 'option B preview' },
        ],
      }),
    ])

    expect(wrapper.find('[data-testid="ask-option-preview"]').exists()).toBe(false)
    await wrapper.findAll('[data-testid="ask-option"]')[1].trigger('click')
    const preview = wrapper.find('[data-testid="ask-option-preview"]')
    expect(preview.exists()).toBe(true)
    expect(preview.text()).toBe('option B preview')
  })

  it('turns the「其他」row into the field it always was', async () => {
    const { wrapper } = mountAsk([
      question({ id: '范围', prompt: '要动哪些范围？', header: '范围' }),
    ])

    await wrapper.find('[data-testid="ask-other"]').trigger('click')
    const input = wrapper.find('[data-testid="ask-other-input"]')
    expect(input.exists()).toBe(true)
    expect(input.attributes('placeholder')).toBe('其他（可选）')
    // With no submit tab of its own, the field brings its own button.
    expect(wrapper.find('[data-testid="ask-submit"]').exists()).toBe(true)
    await input.setValue('只动 pkg/tui')
    expect(wrapper.emitted('set-other')).toEqual([['范围', '只动 pkg/tui']])
  })

  it('sends the engine the same payload the flat form sent, through the real composable', async () => {
    const answered: { actionId: string; body: unknown }[] = []
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      {
        id: 'act-ask',
        kind: 'user_interaction',
        status: 'pending',
        payloadJson: JSON.stringify({
          questions: [
            {
              id: '范围', prompt: '要动哪些范围？',
              options: [{ id: 'TUI', label: 'TUI' }, { id: 'Web', label: 'Web' }],
              allow_other: true,
            },
            {
              id: '补充', prompt: '还要补充什么？',
              options: [{ id: '写测试', label: '写测试' }, { id: '补文档', label: '补文档' }],
              allow_multiple: true, allow_other: true,
            },
          ],
        }),
        createdAt: 1,
        updatedAt: 1,
      } as ActionRecord,
    ])
    vi.spyOn(forebrainApi, 'actionsAnswer').mockImplementation(async (actionId: string, body: unknown) => {
      answered.push({ actionId, body })
      return { ok: true } as never
    })

    const pending = usePendingActions({ sessionId: ref('s1'), version: ref(0), contextUsedPercent: ref(null) })
    await pending.load()
    await flushPromises()
    expect(pending.pendingAsk.value).toHaveLength(1)

    const actionId = pending.pendingAsk.value[0]!.id
    const wrapper = mount(AskTabs, {
      props: {
        questions: pending.pendingAsk.value[0]!.form.questions,
        answers: pending.answers.value[actionId] ?? {},
        others: pending.others.value[actionId] ?? {},
        submitting: false,
        onSetAnswer: (questionId: string, value: string | string[]) => pending.setAskAnswer(actionId, questionId, value),
        onSetOther: (questionId: string, value: string) => pending.setAskOther(actionId, questionId, value),
        onSubmit: () => { void pending.submitAsk(actionId) },
      },
    })

    // The snake_case payload the gateway stores reads as multi-select on the
    // second question, reached by answering the first.
    await wrapper.findAll('[data-testid="ask-option"]')[0].trigger('click')
    expect(wrapper.text()).toContain('还要补充什么？')
    expect(wrapper.find('[data-testid="ask-option"]').text()).toContain('[ ]')

    const second = wrapper.findAll('[data-testid="ask-option"]')
    await second[0].trigger('click')
    await second[1].trigger('click')
    await wrapper.find('[data-testid="ask-other"]').trigger('click')
    await wrapper.find('[data-testid="ask-other-input"]').setValue('尽量小改')
    await wrapper.find('[data-testid="ask-review-open"]').trigger('click')
    const review = wrapper.find('[data-testid="ask-review"]')
    expect(review.text()).toContain('→ TUI')
    expect(review.text()).toContain('→ 写测试, 补文档, 其他：尽量小改')
    await review.find('[data-testid="ask-submit"]').trigger('click')
    await flushPromises()

    // The wire payload, verbatim: question ids the engine keyed them by, the
    // picked option ids, other text only when it says something.
    expect(answered).toEqual([{
      actionId: 'act-ask',
      body: {
        answers: [
          { questionId: '范围', optionIds: ['TUI'] },
          { questionId: '补充', optionIds: ['写测试', '补文档'], otherText: '尽量小改' },
        ],
      },
    }])
    wrapper.unmount()
  })
})
