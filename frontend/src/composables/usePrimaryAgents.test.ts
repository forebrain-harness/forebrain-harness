import { afterEach, describe, expect, it, vi } from 'vitest'

import { usePrimaryAgents } from './usePrimaryAgents'
import { forebrainApi, type PrimaryAgentRecord } from '@/lib/api'

function agent(id: string, active: boolean): PrimaryAgentRecord {
  return { id, workspaceRoot: `/home/${id}`, privateSkillsRoot: '', sharedSkillsRoots: [], active, status: active ? 'active' : 'available' } as PrimaryAgentRecord
}

describe('usePrimaryAgents', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('a failed switch shows the tenant the gateway recorded, not the one it left', async () => {
    const store = usePrimaryAgents()
    vi.spyOn(forebrainApi, 'primaryAgents')
      .mockResolvedValueOnce({ activeId: 'main', records: [agent('main', true), agent('helper', false)] })
      // The switch recorded its target before its rebuild failed.
      .mockResolvedValueOnce({ activeId: 'helper', records: [agent('main', false), agent('helper', true)] })
    vi.spyOn(forebrainApi, 'primaryAgentSwitch').mockRejectedValue(new Error('runner runtime is uncertain'))
    await store.loadPrimaryAgents()
    const tokenBefore = store.refreshToken.value

    await expect(store.switchPrimaryAgent('helper')).rejects.toThrow('runner runtime is uncertain')

    expect(store.activeId.value).toBe('helper')
    // The tenant moved, so everything loaded under the old one is redrawn.
    expect(store.refreshToken.value).toBe(tokenBefore + 1)
    expect(store.error.value).toBe('runner runtime is uncertain')
  })

  it('a refused switch that recorded nothing keeps the tenant as it was', async () => {
    const store = usePrimaryAgents()
    vi.spyOn(forebrainApi, 'primaryAgents')
      .mockResolvedValue({ activeId: 'main', records: [agent('main', true), agent('helper', false)] })
    vi.spyOn(forebrainApi, 'primaryAgentSwitch').mockRejectedValue(new Error('primary agent "nope" not found'))
    await store.loadPrimaryAgents()
    const tokenBefore = store.refreshToken.value

    await expect(store.switchPrimaryAgent('nope')).rejects.toThrow()

    expect(store.activeId.value).toBe('main')
    expect(store.refreshToken.value).toBe(tokenBefore)
    expect(store.error.value).toBe('primary agent "nope" not found')
  })
})
