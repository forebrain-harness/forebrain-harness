import { beforeAll, describe, expect, it } from 'vitest'

import type { McpServerRow } from '@/lib/api'
import { setLocale } from '@/locales'

import {
  mcpEditPlace,
  mcpFormOf,
  mcpKeyValuePayload,
  mcpOAuthPayload,
  mcpStatus,
  mcpTypeLabel,
  mcpWritePayload,
  MCP_REDACTED,
} from './mcpRows'

beforeAll(() => setLocale('zh'))

function row(overrides: Partial<McpServerRow> = {}): McpServerRow {
  return {
    name: 'docs',
    transport: 'stdio',
    target: '/bin/docs',
    required: false,
    enabled: true,
    startupTimeout: 0,
    requestTimeout: 0,
    headers: {},
    env: {},
    inheritParentEnv: false,
    disableStandaloneSse: false,
    expectedCommandSha256: '',
    defaultToolsApprovalMode: '',
    oauth: {
      mode: '',
      accessToken: '',
      tokenType: '',
      clientId: '',
      clientSecret: '',
      tokenUrl: '',
      authorizationUrl: '',
      redirectUrl: '',
      refreshToken: '',
      scopes: [],
    },
    scope: 'global',
    ...overrides,
  }
}

describe('mcpStatus', () => {
  it('reads an entry awaiting confirmation, ahead of anything else the row says', () => {
    // A pending project entry has never been started, so the failure text of
    // some other state is not what the operator is being asked about.
    expect(mcpStatus(row({ scope: 'project', pending: true, error: 'boom' }))).toEqual({
      tone: 'warn',
      label: '待确认',
      tip: '',
    })
    // The same flag on a layer view means "a reload is pending", not "nobody
    // decided": only the project view hands the operator that decision.
    expect(mcpStatus(row({ scope: 'global', pending: true, running: true }))).toEqual({
      tone: 'ok',
      label: '已连接',
      tip: '',
    })
    // And a project row that already carries its decision is decided, not
    // pending, whatever the flag says.
    expect(mcpStatus(row({ scope: 'project', pending: true, allowed: true })).label).toBe('已允许')
  })

  it('separates "off in the file" from "off for the next session"', () => {
    expect(mcpStatus(row({ disabledByConfig: true, disabledNextSession: true }))).toEqual({
      tone: 'gray',
      label: '配置里已禁用',
      tip: 'enabled: false',
    })
    expect(mcpStatus(row({ disabledNextSession: true }))).toEqual({
      tone: 'gray',
      label: '已禁用',
      tip: '下个会话起不加载',
    })
  })

  it('keeps a failure visible with the server its own words', () => {
    expect(mcpStatus(row({ connStatus: 'failed', error: 'spawn /bin/nope: not found' }))).toEqual({
      tone: 'err',
      label: '连接失败',
      tip: 'spawn /bin/nope: not found',
    })
    // A failure a view could not report a status for is still a failure: the
    // row's error text alone is enough.
    expect(mcpStatus(row({ error: 'refused' })).label).toBe('连接失败')
    expect(mcpStatus(row({ connStatus: 'failed' })).tip).toBe('连接失败')
    // And it outranks the running state: a server that failed is not connected.
    expect(mcpStatus(row({ error: 'refused', running: true })).tone).toBe('err')
  })

  it('carries the tool count only when a count was reported', () => {
    expect(mcpStatus(row({ running: true, toolCount: 5 }))).toEqual({
      tone: 'ok',
      label: '已连接 · 5 个工具',
      tip: '',
    })
    expect(mcpStatus(row({ running: true, toolCount: 0 })).label).toBe('已连接')
    expect(mcpStatus(row({ running: true })).label).toBe('已连接')
    expect(mcpStatus(row({ scope: 'project', allowed: true, toolCount: 3 }))).toEqual({
      tone: 'ok',
      label: '已允许 · 3 个工具',
      tip: '',
    })
    expect(mcpStatus(row({ scope: 'project', allowed: true })).label).toBe('已允许')
  })

  it('says an entry that is configured but not running is not running', () => {
    expect(mcpStatus(row({ running: false }))).toEqual({ tone: 'gray', label: '未运行', tip: '' })
    expect(mcpStatus(row())).toEqual({ tone: 'gray', label: '未运行', tip: '' })
  })

  it('keeps a declined project entry visible, with the way back in its tip', () => {
    // A declined entry no longer loads, but the row stays: 允许 on it records a
    // new decision, and the badge is what says the entry is not simply off.
    expect(mcpStatus(row({ scope: 'project', declined: true, running: false }))).toEqual({
      tone: 'gray',
      label: '已拒绝',
      tip: '不会加载；点“允许”撤回',
    })
    // A declined row is decided, so it outranks what the runtime says about it.
    expect(mcpStatus(row({ scope: 'project', declined: true, error: 'boom' })).label).toBe('已拒绝')
    // Only the project view produces the decision; the flag on another layer's
    // row means nothing there.
    expect(mcpStatus(row({ scope: 'global', declined: true, running: true })).label).toBe('已连接')
  })

  it('names the layer that overrides a shared entry instead of calling it idle', () => {
    expect(mcpStatus(row({ scope: 'global', shadowedBy: 'agent', shadowedByName: 'main' }))).toEqual({
      tone: 'gray',
      label: '未运行 · 被覆盖',
      tip: '被主代理 main 覆盖，未运行',
    })
    // The override outranks the runtime state a row could not have anyway: a
    // shadowed entry is not in the session.
    expect(mcpStatus(row({ scope: 'global', shadowedBy: 'agent', running: false })).label).toBe('未运行 · 被覆盖')
  })

  it('reads the runtime’s whole connection state, not just "it exists"', () => {
    // A connection still being made is not a connection.
    expect(mcpStatus(row({ running: true, connStatus: 'connecting' }))).toEqual({
      tone: 'warn',
      label: '连接中',
      tip: '',
    })
    // A dropped one, a skipped one and one the view knows nothing about are all
    // "not running", never "connected".
    expect(mcpStatus(row({ running: true, connStatus: 'disconnected' })).label).toBe('未运行')
    expect(mcpStatus(row({ running: true, connStatus: 'cancelled' })).label).toBe('未运行')
    expect(mcpStatus(row({ running: true, connStatus: 'unknown' })).label).toBe('未运行')
    // A view that could ask no runner carries no state at all; a row that is in
    // the session is still connected.
    expect(mcpStatus(row({ running: true })).label).toBe('已连接')
    expect(mcpStatus(row({ running: true, connStatus: 'connected' })).label).toBe('已连接')
  })

  it('puts an OAuth state in the tip, not the badge', () => {
    expect(mcpStatus(row({ running: true, authStatus: 'needs-auth' }))).toEqual({
      tone: 'ok',
      label: '已连接',
      tip: 'OAuth 需要授权',
    })
    expect(mcpStatus(row({ running: true, authStatus: 'failed' }))).toEqual({
      tone: 'ok',
      label: '已连接',
      tip: 'OAuth 授权失败',
    })
    // A failure keeps its own text, with the OAuth state after it.
    expect(mcpStatus(row({ error: 'unauthorized', authStatus: 'needs-auth' })).tip).toBe(
      'unauthorized · OAuth 需要授权',
    )
    // An authenticated or unknown state adds nothing.
    expect(mcpStatus(row({ running: true, authStatus: 'authenticated' })).tip).toBe('')
  })
})

describe('mcpTypeLabel', () => {
  it('names only the transport, and nothing it carries', () => {
    // The 类型 column says which transport an entry uses; the OAuth flow and the
    // headers it carries are the drawer's (`plan 079`).
    expect(mcpTypeLabel(row())).toBe('stdio')
    expect(mcpTypeLabel(row({ transport: 'streamable_http' }))).toBe('http')
    expect(mcpTypeLabel(row({ headers: { Authorization: MCP_REDACTED } }))).toBe('stdio')
    expect(mcpTypeLabel(row({ oauthConfigured: true, headers: { 'X-Api-Key': 'k' } }))).toBe('stdio')
    expect(mcpTypeLabel(row({ transport: 'streamable_http', oauth: { ...row().oauth, mode: 'refresh' } }))).toBe('http')
  })
})

describe('mcpEditPlace', () => {
  it('sends each layer to the page that edits it', () => {
    expect(mcpEditPlace('global')).toEqual({ to: '/settings?tab=mcp', label: '在“设置 → MCP”修改' })
    expect(mcpEditPlace('agent')).toEqual({ to: '/mcp', label: '在菜单栏“MCP”修改' })
    expect(mcpEditPlace('project', 'p 1')).toEqual({
      to: '/projects/p%201/mcp',
      label: '在项目空间修改',
    })
  })

  it('offers no link for a project it was not given', () => {
    expect(mcpEditPlace('project')).toEqual({ to: '', label: '在项目空间修改' })
  })
})

describe('mcpKeyValuePayload', () => {
  it('sends nothing when the table asks for nothing', () => {
    expect(mcpKeyValuePayload({ TOKEN: MCP_REDACTED }, [{ key: 'TOKEN', value: MCP_REDACTED }])).toBeNull()
    expect(mcpKeyValuePayload(undefined, [])).toBeNull()
    expect(mcpKeyValuePayload(undefined, [{ key: '', value: '' }])).toBeNull()
  })

  it('keeps, edits and removes one key at a time', () => {
    expect(
      mcpKeyValuePayload({ KEEP: MCP_REDACTED, EDIT: MCP_REDACTED, DROP: MCP_REDACTED }, [
        { key: 'KEEP', value: MCP_REDACTED },
        { key: 'EDIT', value: 'new' },
      ]),
    ).toEqual({ KEEP: MCP_REDACTED, EDIT: 'new', DROP: '' })
    // Deleting the only key is a change, so the map travels with the removal.
    expect(mcpKeyValuePayload({ GONE: 'x' }, [])).toEqual({ GONE: '' })
    // A key that is new and blank is a row nobody filled in, not a removal.
    expect(mcpKeyValuePayload({ A: '1' }, [{ key: 'A', value: '1' }, { key: 'B', value: '' }])).toBeNull()
  })

  it('sends every key of a new entry', () => {
    expect(mcpKeyValuePayload(undefined, [{ key: 'A', value: '1' }, { key: 'B', value: '2' }])).toEqual({
      A: '1',
      B: '2',
    })
  })

  it('trims a key a person typed with spaces around it', () => {
    expect(mcpKeyValuePayload({ A: '1' }, [{ key: ' A ', value: '2' }])).toEqual({ A: '2' })
  })
})

describe('mcpOAuthPayload', () => {
  it('sends nothing when the form did not touch the block', () => {
    const stored = row().oauth
    expect(
      mcpOAuthPayload(stored, {
        mode: stored.mode,
        authorizationUrl: stored.authorizationUrl,
        tokenUrl: stored.tokenUrl,
        clientId: stored.clientId,
        clientSecret: stored.clientSecret,
        scopes: stored.scopes.join(' '),
        redirectUrl: stored.redirectUrl,
      }),
    ).toBeNull()
  })

  it('sends the whole block, with the credentials it does not edit kept', () => {
    // The gateway replaces the block wholesale, so a field the drawer does not
    // show has to travel back as the placeholder — dropping it would clear a
    // token the operator never saw.
    const stored = row({
      oauth: {
        ...row().oauth,
        mode: 'refresh',
        accessToken: MCP_REDACTED,
        refreshToken: MCP_REDACTED,
        tokenType: 'Bearer',
        clientId: 'old-id',
        clientSecret: MCP_REDACTED,
        tokenUrl: 'https://auth/token',
        scopes: ['read'],
      },
    }).oauth
    expect(
      mcpOAuthPayload(stored, {
        mode: 'refresh',
        authorizationUrl: '',
        tokenUrl: 'https://auth/token',
        clientId: 'new-id',
        clientSecret: MCP_REDACTED,
        scopes: 'read write',
        redirectUrl: '',
      }),
    ).toEqual({
      mode: 'refresh',
      access_token: MCP_REDACTED,
      token_type: 'Bearer',
      client_id: 'new-id',
      client_secret: MCP_REDACTED,
      token_url: 'https://auth/token',
      authorization_url: '',
      redirect_url: '',
      refresh_token: MCP_REDACTED,
      scopes: ['read', 'write'],
    })
  })

  it('omits an empty block', () => {
    expect(
      mcpOAuthPayload(null, {
        mode: '',
        authorizationUrl: '',
        tokenUrl: '',
        clientId: '',
        clientSecret: '',
        scopes: '   ',
        redirectUrl: '',
      }),
    ).toBeNull()
  })

  it('treats reordered scopes as a change', () => {
    const stored = row({ oauth: { ...row().oauth, scopes: ['a', 'b'] } }).oauth
    expect(
      mcpOAuthPayload(stored, {
        mode: '',
        authorizationUrl: '',
        tokenUrl: '',
        clientId: '',
        clientSecret: '',
        scopes: 'b a',
        redirectUrl: '',
      })?.scopes,
    ).toEqual(['b', 'a'])
  })
})

describe('mcpWritePayload', () => {
  it('sends everything a new entry needs, and nothing it does not', () => {
    const form = {
      ...mcpFormOf(null),
      name: 'playwright',
      transport: 'stdio' as const,
      target: 'npx @playwright/mcp@latest',
      env: [{ key: 'BROWSER', value: 'chromium' }],
      requestTimeout: '30',
    }
    expect(mcpWritePayload(null, form)).toEqual({
      name: 'playwright',
      transport: 'stdio',
      target: 'npx @playwright/mcp@latest',
      required: false,
      enabled: true,
      startup_timeout: 0,
      request_timeout: 30,
      inherit_parent_env: false,
      disable_standalone_sse: false,
      expected_command_sha256: '',
      default_tools_approval_mode: '',
      env: { BROWSER: 'chromium' },
    })
  })

  it('sends only the name and transport when the form was not touched', () => {
    // The strongest form of the diff rule: a drawer opened on an entry and
    // saved unchanged submits no field at all, so nothing on disk moves — the
    // per-tool approval table, which has no form field, first among them.
    const original = row({
      required: true,
      startupTimeout: 10,
      requestTimeout: 60,
      env: { API_KEY: MCP_REDACTED },
      headers: { Authorization: MCP_REDACTED },
      expectedCommandSha256: 'abc',
      defaultToolsApprovalMode: 'prompt',
      oauth: { ...row().oauth, mode: 'static', accessToken: MCP_REDACTED },
    })
    expect(mcpWritePayload(original, mcpFormOf(original))).toEqual({ name: 'docs', transport: 'stdio' })
  })

  it('sends the fields that changed and leaves the rest out', () => {
    const original = row({ requestTimeout: 30, startupTimeout: 10 })
    const form = { ...mcpFormOf(original), requestTimeout: '45', enabled: false }
    expect(mcpWritePayload(original, form)).toEqual({
      name: 'docs',
      transport: 'stdio',
      enabled: false,
      request_timeout: 45,
    })
  })

  it('sends the transport and the target when the transport changed', () => {
    const original = row()
    const form = { ...mcpFormOf(original), transport: 'streamable_http' as const, target: 'https://mcp.test/mcp' }
    expect(mcpWritePayload(original, form)).toEqual({
      name: 'docs',
      transport: 'streamable_http',
      target: 'https://mcp.test/mcp',
    })
  })

  it('reads an emptied timeout as the configuration default', () => {
    const original = row({ startupTimeout: 15 })
    expect(mcpWritePayload(original, { ...mcpFormOf(original), startupTimeout: '  ' })).toEqual({
      name: 'docs',
      transport: 'stdio',
      startup_timeout: 0,
    })
    // A timeout that cannot be a number is the same empty field.
    expect(mcpWritePayload(original, { ...mcpFormOf(original), startupTimeout: 'abc' })).toEqual({
      name: 'docs',
      transport: 'stdio',
      startup_timeout: 0,
    })
  })

  it('carries the tables and the OAuth block only when they changed', () => {
    const original = row({
      env: { KEEP: MCP_REDACTED, DROP: MCP_REDACTED },
      headers: { Authorization: MCP_REDACTED },
    })
    const form = { ...mcpFormOf(original), env: [{ key: 'KEEP', value: MCP_REDACTED }] }
    // Only the map that changed travels, and the OAuth form was untouched, so
    // the block stays off the wire entirely.
    expect(mcpWritePayload(original, form)).toEqual({
      name: 'docs',
      transport: 'stdio',
      env: { KEEP: MCP_REDACTED, DROP: '' },
    })
  })

  it('speaks the interface language, not a hard-coded one', () => {
    setLocale('en')
    try {
      expect(mcpStatus(row()).label).toBe('Not running')
      expect(mcpEditPlace('global').label).toBe('Change it in Settings → MCP')
    } finally {
      setLocale('zh')
    }
  })

  it('reads the runtime\'s own error status as a failed connection', () => {
    // The registry spells a failed connection "error"; a row carrying it is
    // red even before it has any text to explain itself.
    expect(mcpStatus(row({ connStatus: 'error' }))).toMatchObject({ tone: 'err', label: '连接失败' })
  })
})
