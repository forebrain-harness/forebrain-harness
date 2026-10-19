import { afterEach, describe, expect, it, vi } from 'vitest'

import { forebrainApi, __apiClient, getErrorMessage } from './api'

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

/**
 * A subagent write lands in forebrain.yaml, whose fields are snake_case. The
 * page's own spelling would be stored as unknown fields and lost, so the body
 * the request carries is the file's — and the values inside it, a prompt and a
 * tool name among them, stay exactly as the form typed them.
 */
describe('subagent configuration writes', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('writes a subagent in the file\'s own spelling', async () => {
    const put = vi.spyOn(__apiClient, 'put').mockResolvedValueOnce({ data: { name: 'db-migrator' } })

    await forebrainApi.updateSubagentConfig('review', 'db-migrator', {
      description: 'Writes and checks migrations',
      model: 'deepseek/deepseek-v4',
      reasoningEffort: 'high',
      systemPrompt: 'You write migrations.',
      tools: 'pick',
      allowedTools: ['read_file', 'edit_file'],
      continuable: true,
      resetPrompt: true,
    })

    expect(put).toHaveBeenCalledWith('/agents/primary/review/subagents/db-migrator', {
      description: 'Writes and checks migrations',
      model: 'deepseek/deepseek-v4',
      reasoning_effort: 'high',
      system_prompt: 'You write migrations.',
      tools: 'pick',
      allowed_tools: ['read_file', 'edit_file'],
      continuable: true,
      reset_prompt: true,
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
 * A decision on the project page names the one entry the click is about, and
 * the endpoint records a decision for that entry alone. Without `decide` the
 * request keeps the old shape — the whole undecided set — which is what the
 * terminal's startup prompt still sends.
 */
describe('project consent requests', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('names the entry a click decides with its fingerprint untouched, and sends no decide key when it decides a whole batch', async () => {
    const post = vi.spyOn(__apiClient, 'post').mockResolvedValue({ data: { ok: true, allowed: [] } })

    await forebrainApi.projectMcpConsent('p', ['my-Server'], ['my-Server'], [{ name: 'my-Server', fingerprint: 'fp-1' }])
    await forebrainApi.projectMcpConsent('p', ['x'])

    // The fingerprint travels as an array: postJson snake-cases object keys,
    // so an object keyed by the entry name would corrupt `my-Server` into
    // `my_server` before it left the page.
    expect(post).toHaveBeenNthCalledWith(1, '/v1/projects/p/mcp/consent', {
      allow: ['my-Server'],
      decide: ['my-Server'],
      fingerprints: [{ name: 'my-Server', fingerprint: 'fp-1' }],
    })
    expect(post).toHaveBeenNthCalledWith(2, '/v1/projects/p/mcp/consent', { allow: ['x'] })
  })

  it('carries the decision on the language servers’ endpoint too', async () => {
    const post = vi.spyOn(__apiClient, 'post').mockResolvedValue({ data: { ok: true, allowed: ['x'] } })

    await forebrainApi.projectLspConsent('p', ['gopls'], ['gopls'], [{ name: 'gopls', fingerprint: 'fp-2' }])
    await forebrainApi.projectLspConsent('p', [])

    expect(post).toHaveBeenNthCalledWith(1, '/v1/projects/p/lsp/consent', {
      allow: ['gopls'],
      decide: ['gopls'],
      fingerprints: [{ name: 'gopls', fingerprint: 'fp-2' }],
    })
    expect(post).toHaveBeenNthCalledWith(2, '/v1/projects/p/lsp/consent', { allow: [] })
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

describe('getErrorMessage', () => {
  it('surfaces the gateway error body verbatim', () => {
    const error = { response: { data: { error: 'invalid gateway token' } } }
    expect(getErrorMessage(error)).toBe('invalid gateway token')
  })

  it('still reads the message field other backends use', () => {
    const error = { response: { data: { message: 'quota exceeded' } } }
    expect(getErrorMessage(error)).toBe('quota exceeded')
  })
})

describe('mcp pages (plan 074)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('keeps the user\'s own env and header names when it reads an entry', async () => {
    // The shared axios interceptor camelCases every key, which would rename
    // CODEGRAPH_HOME to codegraphHome and write a bogus variable back on the
    // next save. The MCP list reads the wire's own spelling.
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        servers: [
          {
            name: 'codegraph',
            transport: 'stdio',
            target: 'codegraph serve',
            required: false,
            enabled: true,
            startup_timeout: 30,
            request_timeout: 0,
            env: { CODEGRAPH_HOME: '${CG_HOME}', X_API_KEY: '[REDACTED]' },
            headers: { 'X-Api-Key': '[REDACTED]' },
            inherit_parent_env: true,
            disable_standalone_sse: false,
            expected_command_sha256: '',
            default_tools_approval_mode: '',
            oauth: { mode: 'none', client_id: 'cid', token_url: 'https://t' },
            scope: 'global',
          },
        ],
        total: 1,
        page: 1,
        page_size: 10,
      }),
    })
    vi.stubGlobal('fetch', fetchMock)

    const answer = await forebrainApi.mcpPage('global', { page: 1, pageSize: 10 })

    const entry = answer.rows[0]
    expect(entry.env).toEqual({ CODEGRAPH_HOME: '${CG_HOME}', X_API_KEY: '[REDACTED]' })
    expect(entry.headers).toEqual({ 'X-Api-Key': '[REDACTED]' })
    expect(entry.inheritParentEnv).toBe(true)
    expect(entry.startupTimeout).toBe(30)
    expect(entry.oauth.clientId).toBe('cid')
    expect(entry.name).toBe('codegraph')
    expect(answer.total).toBe(1)
  })
})

describe('the tool table (plan 073)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('keeps a parameter name as the schema spelled it', async () => {
    // The shared interceptor camelCases every key; an input schema's property
    // names are the model's parameter names, and `file_path` becoming
    // `filePath` would leave the required list pointing at nothing.
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        tools: [
          {
            name: 'read_file',
            description: 'Read one file',
            category: 'filesystem',
            read_only: true,
            input_schema: {
              type: 'object',
              properties: { file_path: { type: 'string' }, 'max-bytes': { type: 'number' } },
              required: ['file_path'],
            },
          },
        ],
        categories: ['filesystem'],
        total: 1,
        page: 1,
        page_size: 10,
      }),
    })
    vi.stubGlobal('fetch', fetchMock)

    const answer = await forebrainApi.toolsPage({ page: 1, pageSize: 10 })

    const schema = answer.rows[0].inputSchema as { properties: Record<string, unknown>; required: string[] }
    expect(Object.keys(schema.properties)).toEqual(['file_path', 'max-bytes'])
    expect(schema.required).toEqual(['file_path'])
    expect(answer.rows[0].readOnly).toBe(true)
    expect(answer.categories).toEqual(['filesystem'])
  })
})
