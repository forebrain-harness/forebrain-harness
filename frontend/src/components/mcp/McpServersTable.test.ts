import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import { hideTip, useTooltipState } from '@/composables/useTooltip'
import tip from '@/directives/tip'
import {
  forebrainApi,
  mcpSetServerDisabled,
  type McpServerRow,
} from '@/lib/api'
import { setLocale } from '@/locales'

import McpServersTable from './McpServersTable.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    mcpSetServerDisabled: vi.fn(),
  }
})

vi.mock('@/composables/useLastSession', () => ({
  lastSessionIdValue: () => 'session-1',
}))

/**
 * One table, three pages (plan 074): the columns, the layer filter and the
 * per-row actions each view draws, the one switch that writes the next
 * session's list, the delete that asks first, and the identity a row keeps when
 * the server answers in another order.
 */
function row(overrides: Partial<McpServerRow> = {}): McpServerRow {
  return {
    name: 'a',
    transport: 'stdio',
    target: 'npx @playwright/mcp@latest',
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
    running: true,
    ...overrides,
  }
}

function routerOf() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/settings', component: { template: '<div />' } },
      { path: '/mcp', component: { template: '<div />' } },
      { path: '/projects/:id/mcp', component: { template: '<div />' } },
    ],
  })
}

const mounted: VueWrapper[] = []

async function mountTable(
  props: { view: 'global' | 'agent' | 'project'; projectId?: string },
  rows: McpServerRow[],
  notApplied: Array<{ name: string; reason: string }> = [],
): Promise<VueWrapper> {
  vi.mocked(forebrainApi.mcpPage).mockResolvedValue({ rows, total: rows.length, page: 1, pageSize: 10, notApplied })
  const wrapper = mount(McpServersTable, {
    props: { agentId: 'primary-1', ...props },
    global: { plugins: [routerOf()], stubs: { teleport: true }, directives: { tip } },
  })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

/** The row whose identity is `layer:name`, as it is drawn right now. */
function rowOf(wrapper: VueWrapper, key: string) {
  const found = wrapper.find(`[data-mcp-row="${key}"]`)
  expect(found.exists(), `row ${key}`).toBe(true)
  return found
}

function headers(wrapper: VueWrapper): string[] {
  return wrapper.findAll('thead th').map((th) => th.text())
}

/** A row's action column: the cell whose first element is the switch, or the
 * slot that keeps its width, and then the actions themselves. */
function actions(wrapper: VueWrapper, key: string) {
  const found = rowOf(wrapper, key).find('.forebrain-row-actions')
  expect(found.exists(), `actions of ${key}`).toBe(true)
  return found
}

describe('McpServersTable', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'mcpPage')
    vi.spyOn(forebrainApi, 'mcpDelete').mockResolvedValue({
      ok: true,
      path: '/home/.forebrain/forebrain.yaml',
      scope: 'global',
      applied: true,
      removed: true,
      server: row(),
    })
    vi.spyOn(forebrainApi, 'projectMcpConsent').mockResolvedValue({ ok: true })
    vi.mocked(mcpSetServerDisabled).mockResolvedValue({ ok: true, reply: 'ok' })
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
    vi.mocked(mcpSetServerDisabled).mockReset()
  })

  it('lists the shared layer alone: no source column, no source filter, 编辑 / 删除 per row', async () => {
    const wrapper = await mountTable({ view: 'global' }, [
      row({ name: 'a' }),
      row({ name: 'b', scope: 'global' }),
    ])

    expect(wrapper.find('[data-testid="mcp-search"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="mcp-source-filter"]').exists()).toBe(false)
    expect(headers(wrapper)).toEqual(['MCP 服务', '命令或地址', '类型', '状态', '操作'])

    expect(wrapper.findAll('[data-mcp-row]')).toHaveLength(2)
    // The enable switch opens the action column: there is no enable column of
    // its own (plan 079, the prototype's `mcpSwCell`).
    const a = actions(wrapper, 'global:a')
    expect(a.element.firstElementChild?.getAttribute('data-testid')).toBe('mcp-enable-global:a')
    expect(rowOf(wrapper, 'global:a').find('[data-testid="mcp-edit-global:a"]').exists()).toBe(true)
    expect(rowOf(wrapper, 'global:a').find('[data-testid="mcp-delete-global:a"]').exists()).toBe(true)
    expect(rowOf(wrapper, 'global:a').find('[data-testid="mcp-view-global:a"]').exists()).toBe(false)

    expect(wrapper.find('[data-testid="mcp-note"]').text()).toBe(
      '对所有主代理生效，新会话起作用。同名条目被主代理或项目整条覆盖。~/.forebrain/forebrain.yaml · agents.defaults.mcp_servers；改动对新会话生效。',
    )
  })

  it('lists the agent and the shared layers: source column and filter, only the agent rows editable', async () => {
    const wrapper = await mountTable({ view: 'agent' }, [
      row({ name: 'b', scope: 'agent' }),
      row({ name: 'a', scope: 'global' }),
    ])

    expect(headers(wrapper)).toEqual(['MCP 服务', '来源', '命令或地址', '类型', '状态', '操作'])
    const filter = wrapper.find('[data-testid="mcp-source-filter"]')
    expect(filter.findAll('option').map((option) => option.text())).toEqual(['全部来源', '主代理', '全局'])

    expect(actions(wrapper, 'agent:b').element.firstElementChild?.getAttribute('data-testid')).toBe('mcp-enable-agent:b')
    expect(rowOf(wrapper, 'agent:b').find('[data-testid="mcp-edit-agent:b"]').exists()).toBe(true)
    expect(rowOf(wrapper, 'agent:b').find('[data-testid="mcp-view-agent:b"]').exists()).toBe(false)

    const foreign = rowOf(wrapper, 'global:a')
    expect(foreign.find('[data-testid="mcp-view-global:a"]').exists()).toBe(true)
    expect(foreign.find('[data-testid="mcp-edit-global:a"]').exists()).toBe(false)
    expect(foreign.find('[data-testid="mcp-delete-global:a"]').exists()).toBe(false)
    // Another layer's row is not this page's to switch: the empty slot holds
    // the switch's width so the icons align down the column.
    expect(foreign.find('[data-testid="mcp-enable-global:a"]').exists()).toBe(false)
    expect(actions(wrapper, 'global:a').element.firstElementChild?.classList.contains('mcp-sw-slot')).toBe(true)

    expect(wrapper.find('[data-testid="mcp-note"]').text()).toBe(
      '本主代理与全局的 MCP 服务，主代理的排在前面；同名时主代理覆盖全局。全局 MCP 在“设置 → MCP”修改，项目 MCP 在各项目空间；改动在新会话生效。',
    )
  })

  it('opens the read-only drawer for another layer’s row', async () => {
    const wrapper = await mountTable({ view: 'agent' }, [row({ name: 'a', scope: 'global' })])

    await rowOf(wrapper, 'global:a').find('[data-testid="mcp-view-global:a"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[role="dialog"] b').text()).toBe('MCP 服务 · a')
    expect(wrapper.find('[role="dialog"] [data-testid="mcp-save"]').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"] [data-testid="mcp-edit-elsewhere"]').text()).toBe('在“设置 → MCP”修改')
  })

  it('lists all three layers and puts a pending entry’s decision first', async () => {
    const wrapper = await mountTable({ view: 'project', projectId: 'proj-1' }, [
      row({ name: 'e', scope: 'project', allowed: false, pending: true, running: false }),
      row({ name: 'd', scope: 'project', allowed: true }),
      row({ name: 'c', scope: 'agent' }),
      row({ name: 'a', scope: 'global' }),
    ])

    expect(headers(wrapper)).toEqual(['MCP 服务', '来源', '命令或地址', '类型', '状态', '操作'])
    expect(wrapper.findAll('[data-mcp-row]').map((tr) => tr.attributes('data-mcp-row'))).toEqual([
      'project:e',
      'project:d',
      'agent:c',
      'global:a',
    ])

    const pending = rowOf(wrapper, 'project:e')
    expect(pending.find('[data-testid="mcp-allow-project:e"]').exists()).toBe(true)
    expect(pending.find('[data-testid="mcp-decline-project:e"]').exists()).toBe(true)
    expect(pending.find('[data-testid="mcp-edit-project:e"]').exists()).toBe(false)
    // A row awaiting a decision has nothing to switch yet: the two buttons are
    // the decision, and the slot before them keeps the switch's width.
    expect(pending.find('[data-testid="mcp-enable-project:e"]').exists()).toBe(false)
    expect(actions(wrapper, 'project:e').element.firstElementChild?.classList.contains('mcp-sw-slot')).toBe(true)
    expect(pending.text()).toContain('待确认')

    const own = rowOf(wrapper, 'project:d')
    expect(own.find('[data-testid="mcp-edit-project:d"]').exists()).toBe(true)
    expect(own.find('[data-testid="mcp-delete-project:d"]').exists()).toBe(true)
    // The layer this page owns keeps its switch, first in the action column.
    expect(actions(wrapper, 'project:d').element.firstElementChild?.getAttribute('data-testid')).toBe('mcp-enable-project:d')

    expect(rowOf(wrapper, 'agent:c').find('[data-testid="mcp-view-agent:c"]').exists()).toBe(true)
    expect(rowOf(wrapper, 'global:a').find('[data-testid="mcp-view-global:a"]').exists()).toBe(true)

    expect(wrapper.find('[data-testid="mcp-note"]').text()).toBe(
      '本项目能加载的 MCP 服务，依次是本项目、主代理、全局的。同名时按 项目 → 主代理 → 全局 覆盖；这里只增删改项目 MCP，全局与主代理的在各自页面改。',
    )
  })

  it('allows one pending entry through the project’s consent endpoint and re-reads the page', async () => {
    const wrapper = await mountTable({ view: 'project', projectId: 'proj-1' }, [
      row({ name: 'e', scope: 'project', allowed: false, pending: true, running: false, fingerprint: 'fp-e' }),
    ])
    expect(vi.mocked(forebrainApi.mcpPage)).toHaveBeenCalledTimes(1)

    await rowOf(wrapper, 'project:e').find('[data-testid="mcp-allow-project:e"]').trigger('click')
    await flushPromises()

    // The request names the one entry the click decides together with the
    // fingerprint its row was rendered from, so the endpoint leaves every
    // other undecided entry alone and records this one as shown.
    expect(vi.mocked(forebrainApi.projectMcpConsent)).toHaveBeenCalledWith('proj-1', ['e'], ['e'], [
      { name: 'e', fingerprint: 'fp-e' },
    ])
    expect(vi.mocked(forebrainApi.mcpPage)).toHaveBeenCalledTimes(2)
    expect(vi.mocked(forebrainApi.mcpPage).mock.calls[1][0]).toBe('project')
    expect(vi.mocked(forebrainApi.mcpPage).mock.calls[1][1]).toMatchObject({ projectId: 'proj-1' })
  })

  it('declines one pending entry, and says nothing else about it', async () => {
    const wrapper = await mountTable({ view: 'project', projectId: 'proj-1' }, [
      row({ name: 'e', scope: 'project', allowed: false, pending: true, running: false, fingerprint: 'fp-e' }),
    ])
    // A pending row's badge carries no tip of its own: the buttons are the
    // decision, and they decide this entry alone.
    hideTip()
    await rowOf(wrapper, 'project:e').find('.forebrain-badge').trigger('mouseenter')
    expect(useTooltipState().text).toBe('')

    await rowOf(wrapper, 'project:e').find('[data-testid="mcp-decline-project:e"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.projectMcpConsent)).toHaveBeenCalledWith('proj-1', [], ['e'], [
      { name: 'e', fingerprint: 'fp-e' },
    ])
    expect(vi.mocked(forebrainApi.mcpPage)).toHaveBeenCalledTimes(2)
  })

  it('keeps the server’s 409 text up and re-reads the page when the file changed under it', async () => {
    const wrapper = await mountTable({ view: 'project', projectId: 'proj-1' }, [
      row({ name: 'e', scope: 'project', allowed: false, pending: true, running: false, fingerprint: 'fp-e' }),
    ])
    expect(vi.mocked(forebrainApi.mcpPage)).toHaveBeenCalledTimes(1)
    vi.mocked(forebrainApi.projectMcpConsent).mockRejectedValueOnce({
      response: {
        status: 409,
        data: {
          error: '项目文件已变化，请核对后重新确认 / The project file changed; review and confirm again',
          code: 'consent_stale',
          stale: ['e'],
        },
      },
    })

    await rowOf(wrapper, 'project:e').find('[data-testid="mcp-allow-project:e"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.projectMcpConsent)).toHaveBeenCalledWith('proj-1', ['e'], ['e'], [
      { name: 'e', fingerprint: 'fp-e' },
    ])
    // Nothing was recorded: the server's own text stays up, and the rows are
    // asked for again so the entry reappears with its new summary and
    // fingerprint.
    expect(wrapper.text()).toContain('项目文件已变化，请核对后重新确认')
    expect(vi.mocked(forebrainApi.mcpPage)).toHaveBeenCalledTimes(2)
  })

  it('keeps a declined entry in the table with 允许 as its only way back', async () => {
    const wrapper = await mountTable({ view: 'project', projectId: 'proj-1' }, [
      row({ name: 'x', scope: 'project', declined: true, running: false, fingerprint: 'fp-x' }),
    ])

    const declined = rowOf(wrapper, 'project:x')
    expect(declined.text()).toContain('已拒绝')
    // No switch (it does not load) and no decision left to make.
    expect(declined.find('[data-testid="mcp-enable-project:x"]').exists()).toBe(false)
    expect(declined.find('[data-testid="mcp-decline-project:x"]').exists()).toBe(false)
    expect(declined.find('[data-testid="mcp-edit-project:x"]').exists()).toBe(false)

    // The badge says how to take the decision back.
    hideTip()
    await declined.find('.forebrain-badge').trigger('mouseenter')
    expect(useTooltipState().text).toBe('不会加载；点“允许”撤回')

    const undo = declined.find('[data-testid="mcp-undecline-project:x"]')
    expect(undo.exists()).toBe(true)
    // The empty slot ahead of it keeps the switch's width, and the one after it
    // keeps the 编辑 / 删除 slot the other own rows have.
    expect(actions(wrapper, 'project:x').element.firstElementChild?.classList.contains('mcp-sw-slot')).toBe(true)
    expect(declined.findAll('.forebrain-icon-slot')).toHaveLength(2)

    await undo.trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.projectMcpConsent)).toHaveBeenCalledWith('proj-1', ['x'], ['x'], [
      { name: 'x', fingerprint: 'fp-x' },
    ])
  })

  it('names the layer that overrides a shared row', async () => {
    const wrapper = await mountTable({ view: 'global' }, [
      row({ name: 'b', shadowedBy: 'agent', shadowedByName: 'main', running: false }),
    ])

    expect(rowOf(wrapper, 'global:b').text()).toContain('未运行 · 被覆盖')
    expect(rowOf(wrapper, 'global:b').text()).not.toContain('已连接')
  })

  it('switches the next session’s list through the disable store', async () => {
    const wrapper = await mountTable({ view: 'global' }, [row({ name: 'a' })])

    await actions(wrapper, 'global:a').find('[data-testid="mcp-enable-global:a"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(mcpSetServerDisabled)).toHaveBeenCalledWith('a', true, 'session-1')
    expect(vi.mocked(forebrainApi.mcpPage)).toHaveBeenCalledTimes(2)
  })

  it('disables the switch of an entry the configuration turned off, and says why', async () => {
    const wrapper = await mountTable({ view: 'global' }, [
      row({ name: 'a', enabled: false, disabledByConfig: true, running: false }),
    ])

    const toggle = wrapper.find('[data-testid="mcp-enable-global:a"]')
    expect(toggle.attributes('disabled')).toBeDefined()
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(rowOf(wrapper, 'global:a').text()).toContain('配置里已禁用')
  })

  it('asks before deleting, then deletes the row it named', async () => {
    const wrapper = await mountTable({ view: 'global' }, [row({ name: 'a' })])

    await wrapper.find('[data-testid="mcp-delete-global:a"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[role="dialog"] .forebrain-modal-title').text()).toBe('删除 MCP 服务 a？')
    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.mcpDelete)).toHaveBeenCalledWith('global', '', 'a')
    expect(vi.mocked(forebrainApi.mcpPage)).toHaveBeenCalledTimes(2)
  })

  it('keeps every row bound to its own entry when the server answers in another order', async () => {
    const wrapper = await mountTable({ view: 'global' }, [row({ name: 'a' }), row({ name: 'b' })])

    // The same two entries, the other way round: the row that is drawn first is
    // now `b`, and the click must still name `b`.
    vi.mocked(forebrainApi.mcpPage).mockResolvedValue({
      rows: [row({ name: 'b' }), row({ name: 'a' })],
      total: 2,
      page: 1,
      pageSize: 10,
      notApplied: [],
    })
    await wrapper.find('[data-testid="mcp-refresh"]').trigger('click')
    await flushPromises()

    expect(wrapper.findAll('[data-mcp-row]').map((tr) => tr.attributes('data-mcp-row'))).toEqual([
      'global:b',
      'global:a',
    ])

    await wrapper.find('[data-testid="mcp-delete-global:b"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="dialog"] .forebrain-modal-title').text()).toBe('删除 MCP 服务 b？')
    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(vi.mocked(forebrainApi.mcpDelete)).toHaveBeenCalledWith('global', '', 'b')
  })

  it("says which of a project file's entries did not apply", async () => {
    // The preview reports them beside the list; a file that silently loads
    // nothing is exactly what a reader needs to see (the old tab listed them).
    const wrapper = await mountTable(
      { view: 'project', projectId: 'proj-1' },
      [row({ scope: 'project', name: 'd', allowed: true })],
      [{ name: 'broken', reason: 'unknown transport "grpc"' }],
    )
    const note = wrapper.find('[data-testid="mcp-not-applied"]')
    expect(note.exists()).toBe(true)
    expect(note.text()).toContain('broken — unknown transport "grpc"')
  })
})
