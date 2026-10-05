import { afterEach, describe, expect, it, vi } from 'vitest'

import { useSessionInfo, sessionHeaderTitle } from './useSessionInfo'
import { forebrainApi, type ChatSessionInfo } from '@/lib/api'

function info(over: Partial<ChatSessionInfo> = {}): ChatSessionInfo {
  return { id: 'sid', title: 'The title', project: null, ...over }
}

describe('useSessionInfo', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("reads the open conversation's facts by id", async () => {
    vi.spyOn(forebrainApi, 'chatSession').mockResolvedValue(
      info({ title: 'Named by the first message', project: { id: 'p1', name: 'proj' } }),
    )
    const store = useSessionInfo()
    await store.loadSessionInfo('sid')

    expect(forebrainApi.chatSession).toHaveBeenCalledWith('sid')
    expect(store.sessionInfo.value?.title).toBe('Named by the first message')
    expect(store.sessionInfo.value?.project).toEqual({ id: 'p1', name: 'proj' })
  })

  it('a session without a title yet is named by the placeholder the surface chose', () => {
    expect(sessionHeaderTitle(info({ title: '' }), '对话')).toBe('对话')
    expect(sessionHeaderTitle(null, 'Chat')).toBe('Chat')
    expect(sessionHeaderTitle(info({ title: 'Kept' }), '对话')).toBe('Kept')
  })

  it('a stale answer never overwrites the conversation now open', async () => {
    let resolveFirst!: (value: ChatSessionInfo) => void
    vi.spyOn(forebrainApi, 'chatSession')
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValueOnce(info({ id: 'two', title: 'Second' }))
    const store = useSessionInfo()
    const slow = store.loadSessionInfo('one')
    const fast = store.loadSessionInfo('two')
    await fast
    resolveFirst(info({ id: 'one', title: 'First' }))
    await slow

    expect(store.sessionInfo.value?.id).toBe('two')
  })

  it('no session open holds no facts', async () => {
    const spy = vi.spyOn(forebrainApi, 'chatSession')
    const store = useSessionInfo()
    await store.loadSessionInfo(null)

    expect(store.sessionInfo.value).toBeNull()
    expect(spy).not.toHaveBeenCalled()
  })
})
