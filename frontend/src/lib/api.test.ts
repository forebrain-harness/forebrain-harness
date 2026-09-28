import { afterEach, describe, expect, it, vi } from 'vitest'

import { forebrainApi, __apiClient } from './api'

describe('forebrain api primary agents', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('loads primary agents from the gateway and returns camel case data', async () => {
    const get = vi.spyOn(__apiClient, 'get').mockResolvedValueOnce({
      data: {
        activeId: 'review',
        records: [{ id: 'review', workspaceRoot: '/tmp/review', active: true }],
      },
    })

    const res = await forebrainApi.primaryAgents()

    expect(get).toHaveBeenCalledWith('/agents/primary')
    expect(res.activeId).toBe('review')
    expect(res.records[0].workspaceRoot).toBe('/tmp/review')
  })

  it('switches primary agents with a snake-case request body', async () => {
    const post = vi.spyOn(__apiClient, 'post').mockResolvedValueOnce({
      data: { activeId: 'review', records: [] },
    })

    const res = await forebrainApi.primaryAgentSwitch('review')

    expect(post).toHaveBeenCalledWith('/agents/primary/switch', { agent_id: 'review' })
    expect(res.activeId).toBe('review')
  })

  it('loads the live agent roster from the gateway', async () => {
    const get = vi.spyOn(__apiClient, 'get').mockResolvedValueOnce({
      data: {
        records: [
          {
            id: 'review',
            kind: 'primary',
            label: 'review',
            status: 'running',
            sessionId: 'session-1',
            elapsedSeconds: 12,
          },
        ],
      },
    })

    const res = await forebrainApi.agentRoster()

    expect(get).toHaveBeenCalledWith('/agents/roster')
    expect(res.records[0].sessionId).toBe('session-1')
    expect(res.records[0].elapsedSeconds).toBe(12)
  })

  it('cancels primary and subagent rows through encoded gateway paths', async () => {
    const post = vi.spyOn(__apiClient, 'post')
      .mockResolvedValueOnce({ data: { ok: true, cancelled: true } })
      .mockResolvedValueOnce({ data: { ok: true, cancelled: true } })

    const primary = await forebrainApi.primaryAgentCancel('agent/review')
    const subagent = await forebrainApi.subagentCancel('child/one')

    expect(post).toHaveBeenNthCalledWith(1, '/agents/agent%2Freview/cancel', {})
    expect(post).toHaveBeenNthCalledWith(2, '/subagents/child%2Fone/cancel', {})
    expect(primary.cancelled).toBe(true)
    expect(subagent.cancelled).toBe(true)
  })

  it('cancels all running agents through the gateway', async () => {
    const post = vi.spyOn(__apiClient, 'post').mockResolvedValueOnce({
      data: { ok: true, main: 1, subagents: 2 },
    })

    const res = await forebrainApi.agentsCancelAll()

    expect(post).toHaveBeenCalledWith('/agents/cancel-all', {})
    expect(res.main).toBe(1)
    expect(res.subagents).toBe(2)
  })

  it('submits approval decisions using the gateway wire format', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ id: 'action-1', kind: 'shell', status: 'approved', payload_json: '{}', created_at: 1, updated_at: 2 }),
    })
    vi.stubGlobal('fetch', fetchMock)

    await forebrainApi.actionsApprove('action/1', {
      decision: 'accept_with_execpolicy_amendment',
      execpolicyAmendment: ['printf', '%s', ''],
    })

    expect(fetchMock).toHaveBeenCalledOnce()
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(path).toBe('/api/actions/action%2F1/approve')
    expect(JSON.parse(String(init.body))).toEqual({
      reason: '',
      decision: 'accept_with_execpolicy_amendment',
      execpolicy_amendment: ['printf', '%s', ''],
    })
  })
})

describe('mcp server status', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('asks for one session\'s servers when a session id is given', async () => {
    const get = vi.spyOn(__apiClient, 'get').mockResolvedValueOnce({
      data: {
        servers: [{ name: 'docs', connStatus: 'connected', toolCount: 3, required: false }],
        project: {},
        runtimeScope: 'session',
        runtimeAvailable: true,
        generation: 'abc123',
        pending: false,
      },
    })

    const res = await forebrainApi.mcpServersWithStatus({ sessionId: 's-1' })

    // The session is what makes the runtime half this session's own state rather
    // than the primary Runner's.
    expect(get).toHaveBeenCalledWith('/v1/mcp/servers', { params: { session_id: 's-1' } })
    expect(res.runtimeScope).toBe('session')
    expect(res.runtimeAvailable).toBe(true)
    expect(res.servers[0].connStatus).toBe('connected')
    expect(res.servers[0].toolCount).toBe(3)
  })

  it('keeps a failed server in the list with its own error text', async () => {
    vi.spyOn(__apiClient, 'get').mockResolvedValueOnce({
      data: {
        servers: [
          { name: 'broken', connStatus: 'error', error: 'spawn npx: executable file not found in $PATH' },
          { name: 'skipped', connStatus: 'cancelled', required: false },
        ],
        runtimeAvailable: true,
      },
    })

    const res = await forebrainApi.mcpServersWithStatus()

    // An entry that vanished from the list would read as "not configured", which
    // is the opposite of what happened.
    expect(res.servers.map((s) => s.name)).toEqual(['broken', 'skipped'])
    expect(res.servers[0].error).toBe('spawn npx: executable file not found in $PATH')
    expect(res.servers[1].connStatus).toBe('cancelled')
  })

  it('reports the primary scope when no runtime answered', async () => {
    vi.spyOn(__apiClient, 'get').mockResolvedValueOnce({
      data: { servers: [{ name: 'docs' }], runtimeAvailable: false },
    })

    const res = await forebrainApi.mcpServersWithStatus()

    expect(res.runtimeAvailable).toBe(false)
    // No runtime field means no state to show, which is not the same as
    // "connected".
    expect(res.servers[0].connStatus).toBeUndefined()
  })
})

/**
 * Every JSON response goes through the client's camel-case interceptor, so the
 * mention types must name fields as the client delivers them. They were once
 * typed in the server's snake case: every picked image was read as missing and
 * silently dropped, and a picked directory never stayed open to drill into.
 */
describe('forebrain api mentions', () => {
  afterEach(() => {
    __apiClient.defaults.adapter = undefined
  })

  function respondWith(data: unknown) {
    __apiClient.defaults.adapter = async (config) => ({ data, status: 200, statusText: 'OK', headers: {}, config })
  }

  it('delivers an accepted image with the path to attach', async () => {
    respondWith({ draft: 'see ', cursor: 4, image_path: 'shots/diagram.png', keep_open: false })
    const res = await forebrainApi.mentionAccept({ draft: 'see @dia', token_start: 4, token_end: 8, path: 'shots/diagram.png', is_dir: false })
    expect(res.imagePath).toBe('shots/diagram.png')
    expect(res.keepOpen).toBe(false)
  })

  it('delivers directory candidates as directories', async () => {
    respondWith({ query: 'sh', records: [{ path: 'shots/', is_dir: true }, { path: 'shell.go', is_dir: false }] })
    const res = await forebrainApi.mentionSearch('sh')
    expect(res.records.map((record) => record.isDir)).toEqual([true, false])
  })
})
