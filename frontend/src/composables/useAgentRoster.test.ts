import { afterEach, describe, expect, it, vi } from 'vitest'

import { useAgentRoster, agentRosterConversationTarget, agentRosterViewTarget } from './useAgentRoster'
import { forebrainApi, type AgentRosterRow } from '@/lib/api'

describe('useAgentRoster', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('loads roster rows from the gateway', async () => {
    vi.spyOn(forebrainApi, 'agentRoster').mockResolvedValueOnce({
      records: [
        {
          id: 'review',
          kind: 'primary',
          label: 'review',
          status: 'running',
          sessionId: 'session-1',
        },
      ],
    })

    const roster = useAgentRoster()
    await roster.loadRoster()

    expect(roster.records.value).toHaveLength(1)
    expect(roster.records.value[0].sessionId).toBe('session-1')
    expect(roster.error.value).toBe('')
  })

  it('cancels primary and subagent rows then refreshes the roster', async () => {
    const primary: AgentRosterRow = {
      id: 'review',
      kind: 'primary',
      label: 'review',
      status: 'running',
    }
    const subagent: AgentRosterRow = {
      id: 'child-1',
      kind: 'subagent',
      parentId: 'review',
      label: 'child',
      status: 'running',
    }
    const load = vi.spyOn(forebrainApi, 'agentRoster').mockResolvedValue({ records: [] })
    const cancelPrimary = vi.spyOn(forebrainApi, 'primaryAgentCancel').mockResolvedValue({ ok: true, cancelled: true })
    const cancelSubagent = vi.spyOn(forebrainApi, 'subagentCancel').mockResolvedValue({ ok: true, cancelled: true })

    const roster = useAgentRoster()
    await roster.cancelRoster(primary)
    await roster.cancelRoster(subagent)

    expect(cancelPrimary).toHaveBeenCalledWith('review')
    expect(cancelSubagent).toHaveBeenCalledWith('child-1')
    expect(load).toHaveBeenCalledTimes(2)
  })

  it('cancels all rows then refreshes the roster', async () => {
    const cancelAll = vi.spyOn(forebrainApi, 'agentsCancelAll').mockResolvedValue({ ok: true, main: 1, subagents: 1 })
    const load = vi.spyOn(forebrainApi, 'agentRoster').mockResolvedValue({ records: [] })

    const roster = useAgentRoster()
    await roster.cancelAll()

    expect(cancelAll).toHaveBeenCalled()
    expect(load).toHaveBeenCalled()
  })

  it('builds chat view targets only for rows with a live session', () => {
    expect(agentRosterViewTarget({
      id: 'review',
      kind: 'primary',
      label: 'review',
      status: 'running',
      sessionId: 'session-1',
    })).toEqual({ path: '/', query: { session: 'session-1' } })

    expect(agentRosterViewTarget({
      id: 'child-1',
      kind: 'subagent',
      label: 'child',
      status: 'running',
    })).toBeNull()
  })

  it('points a subagent at the conversation it ran in, not at its worker session', () => {
    // 所在会话 is the dialogue the subagent belongs to; its own worker session is
    // a separate conversation the operator never opened.
    expect(agentRosterConversationTarget({
      id: 'child-1',
      kind: 'subagent',
      label: 'child',
      status: 'running',
      sessionId: 'worker-1',
      parentSessionId: 'parent-1',
    })).toEqual({ path: '/', query: { session: 'parent-1' } })
  })

  it('falls back to the row’s own session when it carries no parent', () => {
    // A primary agent is its own conversation.
    expect(agentRosterConversationTarget({
      id: 'review',
      kind: 'primary',
      label: 'review',
      status: 'running',
      sessionId: 'session-1',
    })).toEqual({ path: '/', query: { session: 'session-1' } })

    expect(agentRosterConversationTarget({
      id: 'child-2',
      kind: 'subagent',
      label: 'child',
      status: 'running',
    })).toBeNull()
  })
})
