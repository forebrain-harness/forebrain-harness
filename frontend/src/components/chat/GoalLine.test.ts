import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import type { ForebrainGoalLine } from '@/lib/forebrainGatewayRuntime'
import GoalLine from './GoalLine.vue'

/**
 * A goal's line reads the way the terminal prints it: what the line is, what
 * it amounts to, and in full what it is about. A line the check decided opens
 * that check's view.
 */
describe('GoalLine', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  function line(goal: ForebrainGoalLine) {
    const wrapper = mount(GoalLine, { props: { goal } })
    return {
      wrapper,
      title: wrapper.find('.goal-title').text(),
      detail: wrapper.find('.goal-detail').exists() ? wrapper.find('.goal-detail').text() : '',
      body: wrapper.find('.goal-body').exists() ? wrapper.find('.goal-body').text() : '',
      tone: [...wrapper.find('.goal-line').classes()].find((name) => name.startsWith('is-')),
    }
  }

  it('opens with the objective and how the goal works', () => {
    const got = line({ phase: 'started', objective: 'make the tests pass' })
    expect(got.title).toBe('Goal')
    expect(got.detail).toBe('works in rounds until a check of the workspace finds it met · Esc stops')
    expect(got.body).toBe('make the tests pass')
    expect(got.wrapper.find('button').exists()).toBe(false)
  })

  it('shows a round with the check’s reason and opens that check', async () => {
    const got = line({ phase: 'round', round: 3, why: 'two tests still fail', checkAgentId: 'check-2' })
    expect(got.title).toBe('Round 3')
    expect(got.detail).toBe('not met yet')
    expect(got.body).toBe('two tests still fail')
    await got.wrapper.find('button').trigger('click')
    expect(got.wrapper.emitted('open-check')).toEqual([['check-2']])
  })

  it.each<[Partial<ForebrainGoalLine>, string, string, string]>([
    [{ status: 'done', rounds: 1, durationMs: 3_000 }, 'Goal met', '1 round · 3s', 'is-done'],
    [{ status: 'done', rounds: 4, durationMs: 72_000 }, 'Goal met', '4 rounds · 1m 12s', 'is-done'],
    [{ status: 'stuck', rounds: 2, durationMs: 1_000, why: 'the same edit twice' }, 'Goal stopped: no progress', '2 rounds · 1s', 'is-stopped'],
    [{ status: 'capped', rounds: 100, durationMs: 3_725_000 }, 'Goal stopped: round limit reached', '100 rounds · 1h 02m 05s', 'is-stopped'],
    [{ status: 'interrupted', rounds: 3 }, 'Goal stopped', 'interrupted in round 3', 'is-muted'],
    [{ status: 'failed', rounds: 2, why: 'provider returned 529' }, 'Goal stopped: error', 'after round 2', 'is-failed'],
    [{ status: 'failed', rounds: 2 }, 'Goal stopped: error', 'round 2 failed', 'is-failed'],
  ])('ends %o as %s', (end, title, detail, tone) => {
    const got = line({ phase: 'completed', objective: 'ship', ...end })
    expect(got.title).toBe(title)
    expect(got.detail).toBe(detail)
    expect(got.tone).toBe(tone)
    expect(got.body).toBe(end.why ?? '')
  })
})
