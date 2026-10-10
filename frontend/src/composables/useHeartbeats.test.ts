import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useHeartbeats } from './useHeartbeats'
import { resetTenantScope } from './useTenantScope'
import { forebrainApi, type HeartbeatRecord } from '@/lib/api'

function beat(sessionId: string, extra: Partial<HeartbeatRecord> = {}): HeartbeatRecord {
  return { sessionId, intervalSeconds: 600, prompt: 'anything new?', paused: false, ...extra }
}

describe('useHeartbeats', () => {
  beforeEach(() => resetTenantScope())
  afterEach(() => vi.restoreAllMocks())

  it("files each heartbeat under its own conversation", async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([beat('a', { sessionTitle: 'Alpha' }), beat('b', { paused: true })])
    const store = useHeartbeats()
    await store.refresh()

    expect(store.forSession('a')?.sessionTitle).toBe('Alpha')
    expect(store.forSession('b')?.paused).toBe(true)
    // A conversation without one reads as none, not as another's.
    expect(store.forSession('c')).toBeNull()
    expect(store.forSession(null)).toBeNull()
  })

  it('keeps the listed title when a save answers with the setting alone', async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([beat('a', { sessionTitle: 'Alpha' })])
    vi.spyOn(forebrainApi, 'saveHeartbeat').mockResolvedValue(beat('a', { paused: true }))
    const store = useHeartbeats()
    await store.refresh()
    await store.save({ sessionId: 'a', intervalSeconds: 600, prompt: 'anything new?', paused: true })

    expect(store.forSession('a')).toMatchObject({ paused: true, sessionTitle: 'Alpha' })
  })

  it('lists a new heartbeat first and forgets a cleared one', async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([beat('a')])
    vi.spyOn(forebrainApi, 'saveHeartbeat').mockResolvedValue(beat('b'))
    vi.spyOn(forebrainApi, 'clearHeartbeat').mockResolvedValue({ cleared: 'a' })
    const store = useHeartbeats()
    await store.refresh()
    await store.save({ sessionId: 'b', intervalSeconds: 600, prompt: 'anything new?', paused: false })
    expect(store.records.value.map((row) => row.sessionId)).toEqual(['b', 'a'])

    await store.clear('a')
    expect(store.records.value.map((row) => row.sessionId)).toEqual(['b'])
  })

  it('drops a list read that a later write overtook', async () => {
    let answer: (rows: HeartbeatRecord[]) => void = () => {}
    vi.spyOn(forebrainApi, 'heartbeats').mockImplementation(() => new Promise((resolve) => { answer = resolve }))
    vi.spyOn(forebrainApi, 'saveHeartbeat').mockResolvedValue(beat('a', { paused: true }))
    const store = useHeartbeats()
    const reading = store.refresh()
    await store.save({ sessionId: 'a', intervalSeconds: 600, prompt: 'anything new?', paused: true })
    answer([beat('a')])
    await reading

    expect(store.forSession('a')?.paused).toBe(true)
  })

  it("forgets the previous agent's heartbeats on a tenant switch", async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([beat('a')])
    const store = useHeartbeats()
    await store.refresh()
    resetTenantScope()

    expect(store.records.value).toEqual([])
  })
})
