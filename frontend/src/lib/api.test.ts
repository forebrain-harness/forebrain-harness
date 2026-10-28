import axios, { type InternalAxiosRequestConfig } from 'axios'
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

  it('keeps a plan by sending plan_only, and nothing else', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ id: 'action-2', kind: 'exit_plan_mode', status: 'approved', payload_json: '{}', created_at: 1, updated_at: 2 }),
    })
    vi.stubGlobal('fetch', fetchMock)

    await forebrainApi.actionsApprove('action-2', { planOnly: true })

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(path).toBe('/api/actions/action-2/approve')
    expect(JSON.parse(String(init.body))).toEqual({ reason: '', plan_only: true })
  })
})

/**
 * Deliverables (plans 146/153): the conversation's artifact list, an
 * archive's entries, the bytes a renderer reads, and the LibreOffice probe.
 * Every one of them hangs off the conversation and the file, both encoded.
 */
describe('forebrain api deliverables', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('pages the conversation artifacts and returns camel case rows', async () => {
    const get = vi.spyOn(__apiClient, 'get').mockResolvedValueOnce({
      data: {
        items: [{
          id: 'art-1',
          sessionId: 'session-1',
          runId: 'run-1',
          name: 'Q3 经营分析.docx',
          kind: 'document',
          preview: 'docx',
          size: 253952,
          missing: false,
          changed: true,
          downloadUrl: '/api/chat/sessions/session-1/artifacts/art-1/content?disposition=attachment',
          contentUrl: '/api/chat/sessions/session-1/artifacts/art-1/content?disposition=inline',
        }],
        total: 1,
      },
    })

    const page = await forebrainApi.getSessionArtifacts('session/1', { page: 2, limit: 50 })

    expect(get).toHaveBeenCalledWith('/chat/sessions/session%2F1/artifacts?page=2&limit=50')
    expect(page.total).toBe(1)
    expect(page.items[0].downloadUrl).toContain('disposition=attachment')
    expect(page.items[0].changed).toBe(true)
  })

  it('asks for the first page when no page is named', async () => {
    const get = vi.spyOn(__apiClient, 'get').mockResolvedValueOnce({ data: { items: [], total: 0 } })

    await forebrainApi.getSessionArtifacts('session-1')

    expect(get).toHaveBeenCalledWith('/chat/sessions/session-1/artifacts')
  })

  it('lists an archive directory by its encoded path', async () => {
    const get = vi.spyOn(__apiClient, 'get').mockResolvedValueOnce({
      data: { entries: [{ name: '底稿/', size: 0, dir: true }], total: 1, truncated: false },
    })

    const listing = await forebrainApi.getArtifactEntries('session-1', 'art/1')

    expect(get).toHaveBeenCalledWith('/chat/sessions/session-1/artifacts/art%2F1/entries')
    expect(listing.entries[0].dir).toBe(true)
  })

  it('reads a delivered file\'s bytes inline', async () => {
    const bytes = new Uint8Array([1, 2, 3])
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      arrayBuffer: async () => bytes.buffer,
    })
    vi.stubGlobal('fetch', fetchMock)

    const buffer = await forebrainApi.getArtifactContent('session-1', 'art-1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/chat/sessions/session-1/artifacts/art-1/content?disposition=inline',
      undefined,
    )
    expect(new Uint8Array(buffer)).toEqual(bytes)
  })

  it('reports a missing LibreOffice instead of throwing', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 503, text: async () => '{"error":"libreoffice_unavailable"}' })
    vi.stubGlobal('fetch', fetchMock)

    const state = await forebrainApi.getArtifactPreviewState('session-1', 'art-1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/chat/sessions/session-1/artifacts/art-1/preview.pdf',
      { method: 'HEAD' },
    )
    expect(state).toBe('libreoffice_unavailable')
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
  // The transport this describe borrows and puts back: a test that swaps the
  // client's adapter must hand back the one it found, or every test after it
  // runs against a client with no transport of its own.
  const transport = __apiClient.defaults.adapter
  afterEach(() => {
    __apiClient.defaults.adapter = transport
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

/**
 * A project's type (plan 153). The gateway speaks snake_case and the pages
 * speak camelCase: the dialog's pick has to leave as `project_type`, and the
 * suggestion has to arrive as `projectType`, or the card marked 建议 would
 * silently match nothing and every folder would look untagged.
 */
describe('project types and batch tasks (plan 153)', () => {
  const transport = __apiClient.defaults.adapter
  afterEach(() => {
    vi.restoreAllMocks()
    __apiClient.defaults.adapter = transport
  })

  it('creates a project with its type, and the trust a batch one needs', async () => {
    const post = vi.spyOn(__apiClient, 'post').mockResolvedValue({ data: { id: 'p1' } })

    await forebrainApi.projectCreate({ name: '投行底稿审核', root: '/tmp/ibs', projectType: 'batch', trust: true })

    expect(post).toHaveBeenCalledWith('/v1/projects', {
      name: '投行底稿审核',
      root: '/tmp/ibs',
      project_type: 'batch',
      trust: true,
    })
  })

  it('carries a type change and the notification targets on the project PATCH', async () => {
    const patch = vi.spyOn(__apiClient, 'patch').mockResolvedValue({ data: { id: 'p1' } })

    await forebrainApi.projectUpdate('p1', {
      projectType: 'office',
      taskNotify: [{ channel: 'feishu', target: 'oc_1' }],
    })

    expect(patch).toHaveBeenCalledWith('/v1/projects/p1', {
      project_type: 'office',
      task_notify: [{ channel: 'feishu', target: 'oc_1' }],
    })
  })

  it('reads the type a folder would get, camelCased, with the folder as its query', async () => {
    let seen: { url?: string; params?: unknown } = {}
    __apiClient.defaults.adapter = async (config) => {
      seen = { url: config.url, params: config.params }
      return { data: { project_type: 'coding', source: 'git_worktree' }, status: 200, statusText: 'OK', headers: {}, config }
    }

    const suggestion = await forebrainApi.getProjectDefaultType('/Users/me/repo')

    expect(seen).toEqual({ url: '/v1/projects/default-type', params: { root: '/Users/me/repo' } })
    expect(suggestion).toEqual({ projectType: 'coding', source: 'git_worktree' })
  })

  it('reads one page of a project\'s tasks with the limit the endpoint takes', async () => {
    let seen: { url?: string; params?: unknown } = {}
    __apiClient.defaults.adapter = async (config) => {
      seen = { url: config.url, params: config.params }
      return {
        data: {
          items: [
            {
              id: 'T-1',
              project_id: 'p1',
              title: '审核三季度底稿',
              external_ref: 'IB-0917',
              origin: 'web',
              origin_ref: '',
              input_set: true,
              created_at: 10,
              updated_at: 20,
              latest: { seq: 2, session_id: 'web-1', status: 'stopped', started_at: 11, finished_at: 19, error_code: '' },
              executions: 2,
              artifacts: 3,
            },
          ],
          total: 12,
        },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      }
    }

    const page = await forebrainApi.listProjectTasks('p1', { q: '底稿', status: 'stopped', page: 2, pageSize: 5 })

    expect(seen.url).toBe('/v1/projects/p1/tasks')
    // This endpoint pages by `limit` and answers no page of its own.
    expect((seen.params as URLSearchParams).toString()).toBe('page=2&limit=5&q=%E5%BA%95%E7%A8%BF&status=stopped')
    expect(page.total).toBe(12)
    expect(page.page).toBe(2)
    expect(page.pageSize).toBe(5)
    expect(page.rows[0].latest.sessionId).toBe('web-1')
    expect(page.rows[0].artifacts).toBe(3)
  })

  it('clamps a task page past the end to the page the store answers', async () => {
    __apiClient.defaults.adapter = async (config) => ({
      data: { items: [], total: 12 },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    })

    const page = await forebrainApi.listProjectTasks('p1', { page: 9, pageSize: 5 })

    expect(page.page).toBe(3)
  })

  it('reads one task with the page of its execution history', async () => {
    let seen: { url?: string; params?: unknown } = {}
    __apiClient.defaults.adapter = async (config) => {
      seen = { url: config.url, params: config.params }
      return {
        data: {
          task: {
            id: 'T-1',
            project_id: 'p1',
            title: '审核三季度底稿',
            external_ref: 'IB-0917',
            origin: 'api',
            origin_ref: 'ibs-audit',
            input_set: true,
            created_at: 10,
            updated_at: 20,
          },
          executions: {
            items: [
              { seq: 2, session_id: 'web-2', status: 'stopped', workspace_rel: 'tasks/T-1/2', started_at: 30, finished_at: 42, error_code: '', error_text: '', artifacts: 1 },
            ],
            total: 2,
          },
        },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      }
    }

    const detail = await forebrainApi.getTask('T-1', { page: 1, pageSize: 10 })

    expect(seen.url).toBe('/v1/tasks/T-1')
    expect((seen.params as URLSearchParams).toString()).toBe('page=1&limit=10')
    expect(detail.task?.origin).toBe('api')
    expect(detail.rows[0].workspaceRel).toBe('tasks/T-1/2')
    expect(detail.total).toBe(2)
  })

  it('reads the channel recipients a project has spoken to', async () => {
    let seen = ''
    __apiClient.defaults.adapter = async (config) => {
      seen = String(config.url)
      return {
        data: { items: [{ channel: 'feishu', target: 'oc_1', label: '投行底稿审核组', last_seen: 12 }] },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      }
    }

    const targets = await forebrainApi.getChannelTargets('p1')

    expect(seen).toBe('/v1/projects/p1/channel-targets')
    expect(targets).toEqual([{ channel: 'feishu', target: 'oc_1', label: '投行底稿审核组', lastSeen: 12 }])
  })

  it('names the conversation whose newest run the roster should answer', async () => {
    const get = vi.spyOn(__apiClient, 'get').mockResolvedValue({
      data: { records: [{ id: 'main', kind: 'primary', label: 'main', status: 'running', runId: 'run-7' }] },
    })

    const roster = await forebrainApi.agentRoster('s1')

    expect(get).toHaveBeenCalledWith('/agents/roster', { params: { session_id: 's1' } })
    expect(roster.records[0].runId).toBe('run-7')
  })

  it('opens a web task with its task id, external ref and origin in the file spelling', async () => {
    const post = vi.spyOn(__apiClient, 'post').mockResolvedValue({
      data: {
        id: 'web-1',
        title: '审核三季度底稿',
        task: { id: 'T-1', title: '审核三季度底稿', externalRef: 'IB-0917', origin: 'web' },
        execution: { seq: 1, status: 'pending', workspace: '/tmp/ibs/tasks/T-1/1', workspaceRel: 'tasks/T-1/1' },
      },
    })

    const created = await forebrainApi.createTaskSession('p1', {
      title: '审核三季度底稿',
      taskId: 'T-1',
      externalRef: 'IB-0917',
    })

    // The web door is the page's own: a business system's tasks and a person's
    // are told apart by it, so the request always says which one this is.
    expect(post).toHaveBeenCalledWith('/v1/projects/p1/sessions', {
      title: '审核三季度底稿',
      task_id: 'T-1',
      external_ref: 'IB-0917',
      origin: 'web',
    })
    expect(created.execution.workspaceRel).toBe('tasks/T-1/1')
    expect(created.task.externalRef).toBe('IB-0917')
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

/**
 * Reads made at the same moment are one read. Several pages mount together on
 * this surface and each of them asks the same GET; without this, the gateway
 * answers the identical question as many times as there are askers. It is a
 * merge of requests that are in flight, never a cache: an entry dies with the
 * request that made it, so the next read really does read.
 */
describe('concurrent reads', () => {
  const original = axios.defaults.adapter
  let sent: string[] = []

  /** The transport under the layer: it records what actually went out. */
  function transport(answer: (config: InternalAxiosRequestConfig) => Promise<unknown>) {
    axios.defaults.adapter = async (config) => ({
      data: (await answer(config)) as unknown,
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    })
  }

  afterEach(() => {
    axios.defaults.adapter = original
    vi.restoreAllMocks()
  })

  it('sends one request for two reads asked at the same moment', async () => {
    sent = []
    transport(async () => {
      sent.push('read')
      return { value: 'ok' }
    })

    const [first, second] = await Promise.all([forebrainApi.primaryAgents(), forebrainApi.primaryAgents()])

    expect(sent).toHaveLength(1)
    expect(first).toEqual(second)
  })

  it('sends a second request for a read made after the first one finished', async () => {
    sent = []
    transport(async () => {
      sent.push('read')
      return { value: 'ok' }
    })

    await forebrainApi.primaryAgents()
    await forebrainApi.primaryAgents()

    // Not a cache: the answer is not kept past the request that produced it.
    expect(sent).toHaveLength(2)
  })

  it('keeps reads of different parameters apart', async () => {
    sent = []
    transport(async (config) => {
      sent.push(__apiClient.getUri(config))
      return { task: null, executions: { items: [], total: 0 }, page: 1, page_size: 10, total: 0 }
    })

    await Promise.all([
      forebrainApi.getTask('tsk-1', { page: 1, pageSize: 10 }),
      forebrainApi.getTask('tsk-1', { page: 2, pageSize: 10 }),
    ])

    expect(sent).toHaveLength(2)
    expect(sent[0]).toContain('page=1')
    expect(sent[1]).toContain('page=2')
  })

  it('lets a failed read go out again', async () => {
    sent = []
    transport(async () => {
      sent.push('read')
      throw new Error('the gateway is down')
    })

    await expect(forebrainApi.primaryAgents()).rejects.toThrow('the gateway is down')
    await expect(forebrainApi.primaryAgents()).rejects.toThrow('the gateway is down')

    // Nothing is left in flight by a failure: the next read is a real one.
    expect(sent).toHaveLength(2)
  })

  it('shares one failure between reads asked at the same moment', async () => {
    sent = []
    transport(async () => {
      sent.push('read')
      throw new Error('the gateway is down')
    })

    const answers = await Promise.allSettled([forebrainApi.primaryAgents(), forebrainApi.primaryAgents()])

    expect(sent).toHaveLength(1)
    expect(answers.map((answer) => answer.status)).toEqual(['rejected', 'rejected'])
  })

  it('does not merge a read that can be aborted', async () => {
    sent = []
    transport(async () => {
      sent.push('read')
      return { value: 'ok' }
    })

    const controller = new AbortController()
    await Promise.all([
      __apiClient.get('/agents/primary', { signal: controller.signal }),
      __apiClient.get('/agents/primary', { signal: controller.signal }),
    ])

    expect(sent).toHaveLength(2)
  })
})
