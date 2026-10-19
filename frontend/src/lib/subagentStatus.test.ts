import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import type { SubagentTranscript } from '@/composables/useChatStream'
import { setLocale } from '@/locales'
import { subagentPhase, subagentStatusLabel, subagentTabLabel } from './subagentStatus'

type Status = SubagentTranscript['status']

/**
 * One vocabulary for every surface that shows a subagent: the three phases the
 * drawer, the card and the terminal group them by, the status in words, and the
 * name a tab carries.
 */
describe('subagentStatus', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  it('groups every status into the three phases the surfaces draw', () => {
    const phases: Record<Status, ReturnType<typeof subagentPhase>> = {
      running: 'active',
      waiting_approval: 'active',
      waiting_input: 'active',
      done: 'done',
      failed: 'failed',
      cancelled: 'failed',
      interrupted: 'failed',
    }
    for (const [status, phase] of Object.entries(phases)) {
      expect([status, subagentPhase(status as Status)]).toEqual([status, phase])
    }
  })

  it('says every status in words rather than the raw enum', () => {
    const labels: Record<Status, string> = {
      running: 'running',
      waiting_approval: 'Waiting for approval',
      waiting_input: 'Waiting for input',
      done: 'done',
      failed: 'failed',
      cancelled: 'Cancelled',
      interrupted: 'Interrupted',
    }
    for (const [status, label] of Object.entries(labels)) {
      expect([status, subagentStatusLabel(status as Status)]).toEqual([status, label])
    }
  })

  it('names a tab by type and task, or the type alone without a title', () => {
    expect(subagentTabLabel({ agentType: 'plan-reviewer', title: 'review plan 078' }))
      .toBe('plan-reviewer · review plan 078')
    expect(subagentTabLabel({ agentType: 'plan-reviewer' })).toBe('plan-reviewer')
    // A title of spaces is no title at all.
    expect(subagentTabLabel({ agentType: 'plan-reviewer', title: '  ' })).toBe('plan-reviewer')
    // A record older than the engine's naming rule still has a name to show.
    expect(subagentTabLabel({ agentType: '', title: 'review plan 078' })).toBe('subagent · review plan 078')
  })
})
