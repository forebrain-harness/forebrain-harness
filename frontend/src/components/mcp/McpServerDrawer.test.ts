import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type McpServerRow, type McpTestResult } from '@/lib/api'
import { setLocale } from '@/locales'
import { pickOption, selectedText } from '@/test-utils/formControls'
import { InlineTeleport } from '@/test-utils/portal'

import McpServerDrawer from './McpServerDrawer.vue'
import { MCP_REDACTED } from './mcpRows'

/**
 * The MCP drawer (plan 074): one entry's whole record, editable for the layer
 * the page owns and read-only for any other. These tests hold the four things
 * the drawer promises — a viewer cannot type anywhere and is told where the
 * entry is edited, a save sends only what changed and shows a failure in the
 * server's own words without closing, a connection test reports three states
 * without writing, and the advanced half follows the transport.
 */
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

function mountDrawer(props: Record<string, unknown> = {}): VueWrapper {
  // The drawer's selects are the library's, so their options render where the
  // teleport stub puts them — inline, inside this wrapper. The wrapper is
  // attached to the document so a pick can find them: the helpers look the
  // options up on the document, and an inline portal in a detached node is
  // invisible to them.
  return mount(McpServerDrawer, {
    props: { mode: 'edit', scope: 'global', agentId: 'primary-1', ...props },
    global: { plugins: [routerOf()], stubs: { teleport: InlineTeleport }, directives: { tip } },
    attachTo: document.body,
  })
}

/** One of the drawer's selects, by the test id on its trigger. */
function field(wrapper: VueWrapper, testid: string): HTMLElement {
  return wrapper.find(`[data-testid="${testid}"]`).element as HTMLElement
}

// Each test mounts one or more drawers on the document; a wrapper left behind
// would keep its inline options there for the next test's document-wide lookups.
enableAutoUnmount(afterEach)

describe('McpServerDrawer', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    // The selects measure their viewport with ResizeObserver, which jsdom does
    // not have; scrolling an item into view is not implemented either.
    ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    Element.prototype.scrollIntoView = () => undefined
    vi.spyOn(forebrainApi, 'mcpCreate').mockResolvedValue({
      ok: true,
      path: '/home/.forebrain/forebrain.yaml',
      scope: 'global',
      applied: true,
      removed: false,
      server: row(),
    })
    vi.spyOn(forebrainApi, 'mcpUpdate').mockResolvedValue({
      ok: true,
      path: '/home/.forebrain/forebrain.yaml',
      scope: 'global',
      applied: true,
      removed: false,
      server: row(),
    })
    vi.spyOn(forebrainApi, 'mcpTest').mockResolvedValue({ ok: true, toolCount: 0, elapsedMs: 0 })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('names the record in each mode', () => {
    expect(mountDrawer({ mode: 'new', scope: 'agent', agentId: 'a-1' }).find('.forebrain-drawer-head b').text()).toBe(
      '添加 MCP 服务 · 主代理',
    )
    expect(mountDrawer({ mode: 'new', scope: 'project', projectId: 'p-1' }).find('.forebrain-drawer-head b').text()).toBe(
      '添加 MCP 服务 · 项目',
    )
    expect(mountDrawer({ mode: 'edit', row: row() }).find('.forebrain-drawer-head b').text()).toBe('编辑 MCP 服务 · docs')
    expect(mountDrawer({ mode: 'view', row: row() }).find('.forebrain-drawer-head b').text()).toBe('MCP 服务 · docs')
  })

  it('draws a viewer with nothing to type in, no help text and one footer button', () => {
    const wrapper = mountDrawer({
      mode: 'view',
      row: row({ running: true, connStatus: 'connected', toolCount: 5 }),
    })

    // The shell marks the record read-only; this component only fills it.
    expect(wrapper.find('.forebrain-drawer-head .forebrain-badge').text()).toBe('只读')

    const inputs = wrapper.findAll('input')
    expect(inputs.length).toBeGreaterThan(4)
    expect(inputs.every((input) => input.attributes('readonly') !== undefined)).toBe(true)
    const selects = wrapper.findAll('[data-slot="select-trigger"]')
    expect(selects.length).toBeGreaterThan(1)
    expect(selects.every((select) => select.attributes('disabled') !== undefined)).toBe(true)
    const switches = wrapper.findAll('button[role="switch"]')
    expect(switches.length).toBeGreaterThan(1)
    expect(switches.every((item) => item.attributes('disabled') !== undefined)).toBe(true)

    // No editing hints at all — they explain what a save would do.
    expect(wrapper.findAll('.mcp-hint')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('必需 = 连接失败时本会话不启动')

    // An empty field says 未设置 instead of showing an empty box.
    expect(wrapper.findAll('input').every((input) => input.attributes('placeholder') !== undefined)).toBe(true)

    const foot = wrapper.find('.forebrain-drawer-foot')
    expect(foot.findAll('button').map((button) => button.text())).toEqual(['取消'])
  })

  it('sends a viewer to the page that owns the entry', () => {
    const global = mountDrawer({ mode: 'view', row: row() }).find('[data-testid="mcp-edit-elsewhere"]')
    expect(global.text()).toBe('在“设置 → MCP”修改')
    expect(global.attributes('href')).toBe('/settings?tab=mcp')

    const agent = mountDrawer({ mode: 'view', scope: 'agent', row: row({ scope: 'agent' }) })
    expect(agent.find('[data-testid="mcp-edit-elsewhere"]').text()).toBe('在菜单栏“MCP”修改')

    const project = mountDrawer({ mode: 'view', scope: 'project', projectId: 'p-1', row: row({ scope: 'project' }) })
    const link = project.find('[data-testid="mcp-edit-elsewhere"]')
    expect(link.text()).toBe('在项目空间修改')
    expect(link.attributes('href')).toBe('/projects/p-1/mcp')

    // An editable record tests the connection instead of pointing elsewhere.
    const editable = mountDrawer({ mode: 'edit', row: row() })
    expect(editable.find('[data-testid="mcp-edit-elsewhere"]').exists()).toBe(false)
    expect(editable.find('[data-testid="mcp-test-run"]').text()).toBe('测试连接')
  })

  it('keeps the drawer open and shows the server text when a save fails', async () => {
    vi.mocked(forebrainApi.mcpUpdate).mockRejectedValue(
      new Error('mcp: server "docs" already exists in agents.defaults.mcp_servers'),
    )
    const wrapper = mountDrawer({ mode: 'edit', row: row() })

    await wrapper.find('[data-testid="mcp-save"]').trigger('click')
    await flushPromises()

    const block = wrapper.find('[data-testid="mcp-save-error"]')
    expect(block.exists()).toBe(true)
    expect(block.text()).toContain('没有保存：')
    expect(block.text()).toContain('mcp: server "docs" already exists in agents.defaults.mcp_servers')
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.find('.forebrain-drawer').exists()).toBe(true)
    // And the save can be tried again once the operator changes something.
    expect(wrapper.find('[data-testid="mcp-save"]').attributes('disabled')).toBeUndefined()
  })

  it('sends only what changed, then reports the entry it saved', async () => {
    const saved = row({ target: '/bin/docs2' })
    vi.mocked(forebrainApi.mcpUpdate).mockResolvedValue({
      ok: true,
      path: '/home/.forebrain/forebrain.yaml',
      scope: 'global',
      applied: true,
      removed: false,
      server: saved,
    })
    const wrapper = mountDrawer({ mode: 'edit', row: row({ env: { TOKEN: MCP_REDACTED, KEEP: '1' } }) })

    await wrapper.find('[data-testid="mcp-target"]').setValue('/bin/docs2')
    await wrapper.findAll('[data-kv-value]')[0].setValue('typed')
    await wrapper.find('[data-testid="mcp-save"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.mcpUpdate).toHaveBeenCalledWith('global', '', 'docs', {
      name: 'docs',
      transport: 'stdio',
      target: '/bin/docs2',
      // The touched key goes as typed; the untouched one keeps what is stored,
      // and no field the form left alone travels at all.
      env: { TOKEN: 'typed', KEEP: MCP_REDACTED },
    })
    expect(wrapper.emitted('saved')?.[0][0]).toMatchObject({ name: 'docs', target: '/bin/docs2' })
  })

  it('picks 跟随全局 on the default approval mode and sends the empty string', async () => {
    // The one option whose wire value is the empty string: reka refuses an
    // empty SelectItem value, so the sentinel stands in for it and the form
    // reads it back as `''`. Nothing tested this before the migration, and a
    // sentinel that mapped to `'@empty'` would reach the gateway as a mode it
    // does not know.
    const wrapper = mountDrawer({ mode: 'edit', row: row({ defaultToolsApprovalMode: 'writes' }) })
    await flushPromises()
    expect(selectedText(field(wrapper, 'mcp-default-approval'))).toBe('writes（写操作询问）')

    await pickOption(field(wrapper, 'mcp-default-approval'), '跟随全局（留空）')
    expect(selectedText(field(wrapper, 'mcp-default-approval'))).toBe('跟随全局（留空）')

    await wrapper.find('[data-testid="mcp-save"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.mcpUpdate).toHaveBeenCalledWith('global', '', 'docs', {
      name: 'docs',
      transport: 'stdio',
      default_tools_approval_mode: '',
    })
  })

  it('creates in the layer it was opened for, sending the whole form', async () => {
    const wrapper = mountDrawer({ mode: 'new', scope: 'agent', agentId: 'primary-7' })
    expect(wrapper.find('[data-testid="mcp-state"]').exists()).toBe(false)

    await wrapper.find('[data-testid="mcp-name"]').setValue('graph')
    await wrapper.find('[data-testid="mcp-target"]').setValue('/bin/graph')
    await wrapper.find('[data-testid="mcp-save"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.mcpCreate).toHaveBeenCalledWith(
      'agent',
      'primary-7',
      expect.objectContaining({ name: 'graph', transport: 'stdio', target: '/bin/graph', required: false, enabled: true }),
    )
  })

  it('tests the connection without writing, through running, connected and failed', async () => {
    let answer: ((result: McpTestResult) => void) | undefined
    vi.mocked(forebrainApi.mcpTest).mockImplementation(
      () =>
        new Promise<McpTestResult>((resolve) => {
          answer = resolve
        }),
    )
    const wrapper = mountDrawer({ mode: 'edit', row: row() })

    await wrapper.find('[data-testid="mcp-test-run"]').trigger('click')
    expect(wrapper.find('[data-testid="mcp-test-running"]').text()).toBe('测试中…')
    expect(wrapper.find('[data-testid="mcp-test-run"]').attributes('disabled')).toBeDefined()

    answer?.({ ok: true, toolCount: 12, tools: [{ name: 'search_code' }, { name: 'get_issue' }], elapsedMs: 1300 })
    await flushPromises()

    expect(wrapper.find('[data-testid="mcp-test-ok"]').text()).toBe('✓ 已连接')
    expect(wrapper.find('[data-testid="mcp-test-summary"]').text()).toBe('12 个工具 · 1.3 s')
    expect(wrapper.find('[data-testid="mcp-test-tools"]').exists()).toBe(false)
    await wrapper.find('[data-testid="mcp-test-tools-toggle"]').trigger('click')
    expect(wrapper.find('[data-testid="mcp-test-tools"]').text()).toContain('search_code')
    expect(wrapper.find('[data-testid="mcp-test-tools-toggle"]').text()).toBe('收起工具')

    // A test is not a save: nothing written, nothing closed.
    expect(forebrainApi.mcpTest).toHaveBeenCalledWith('global', '', expect.objectContaining({ name: 'docs', target: '/bin/docs' }))
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(wrapper.find('.forebrain-drawer').exists()).toBe(true)

    vi.mocked(forebrainApi.mcpTest).mockResolvedValue({
      ok: false,
      toolCount: 0,
      elapsedMs: 40,
      error: 'Post "https://mcp.example.com/mcp": dial tcp 10.0.0.7:443: i/o timeout',
    })
    await wrapper.find('[data-testid="mcp-test-run"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="mcp-test-failed"]').text()).toBe('✗ 连接失败')
    expect(wrapper.find('[data-testid="mcp-test-error"]').text()).toContain('i/o timeout')
    expect(wrapper.find('[data-testid="mcp-test-ok"]').exists()).toBe(false)
  })

  it('follows the transport with the advanced half', async () => {
    const wrapper = mountDrawer({ mode: 'edit', row: row({ env: { A: '1' } }) })

    expect(wrapper.find('[data-testid="mcp-advanced"] summary').text()).toContain(
      '高级 · 环境变量、进程环境、命令校验、审批模式',
    )
    expect(wrapper.find('[data-testid="mcp-inherit-parent-env"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="mcp-command-sha256"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-kv-row]')).toHaveLength(1)
    expect(wrapper.find('[data-kv-add]').text()).toBe('添加环境变量')
    expect(wrapper.find('[data-testid="mcp-oauth-mode"]').exists()).toBe(false)

    await pickOption(field(wrapper, 'mcp-transport'), 'http')

    const advanced = wrapper.find('[data-testid="mcp-advanced"]')
    expect(advanced.find('summary').text()).toContain('高级 · 请求头、SSE、OAuth、审批模式')
    expect(wrapper.find('[data-testid="mcp-disable-standalone-sse"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="mcp-oauth-mode"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="mcp-oauth-authorization-url"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="mcp-oauth-client-id"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="mcp-inherit-parent-env"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="mcp-command-sha256"]').exists()).toBe(false)
    expect(wrapper.find('[data-kv-add]').text()).toBe('添加请求头')
  })

  it('does not close while a save is in flight', async () => {
    let answer: ((result: Awaited<ReturnType<typeof forebrainApi.mcpUpdate>>) => void) | undefined
    vi.mocked(forebrainApi.mcpUpdate).mockImplementation(
      () =>
        new Promise((resolve) => {
          answer = resolve
        }),
    )
    const wrapper = mountDrawer({ mode: 'edit', row: row() })

    await wrapper.find('[data-testid="mcp-save"]').trigger('click')
    expect(wrapper.find('[data-testid="mcp-save"]').text()).toBe('保存中…')
    expect(wrapper.find('[data-testid="mcp-save"]').attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="mcp-cancel"]').trigger('click')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('close')).toBeUndefined()

    answer?.({
      ok: true,
      path: '/home/.forebrain/forebrain.yaml',
      scope: 'global',
      applied: true,
      removed: false,
      server: row(),
    })
    await flushPromises()
    expect(wrapper.emitted('saved')).toHaveLength(1)

    // Once the answer is in, the drawer closes the ordinary way again.
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('carries the runtime facts a viewer needs in the status box', async () => {
    const wrapper = mountDrawer({
      mode: 'view',
      row: row({
        running: true,
        connStatus: 'connected',
        toolCount: 3,
        authStatus: 'needs-auth',
        officialUrl: true,
        tools: [{ name: 'search_code' }],
      }),
    })

    const box = wrapper.find('[data-testid="mcp-state"]')
    expect(box.text()).toContain('来源')
    expect(box.text()).toContain('全局')
    expect(wrapper.find('[data-testid="mcp-state-status"]').text()).toBe('已连接 · 3 个工具')
    expect(wrapper.find('[data-testid="mcp-state-oauth"]').text()).toBe('需要授权')
    expect(wrapper.find('[data-testid="mcp-state-official"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="mcp-state-tools-toggle"]').text()).toBe('工具列表')
    await wrapper.find('[data-testid="mcp-state-tools-toggle"]').trigger('click')
    expect(wrapper.find('[data-testid="mcp-state-tools"]').text()).toBe('search_code')

    const failing = mountDrawer({
      mode: 'view',
      scope: 'agent',
      agentId: 'a-1',
      row: row({ scope: 'agent', error: 'spawn /bin/nope: not found', disabledNextSession: true }),
    })
    expect(failing.find('[data-testid="mcp-state-error"]').text()).toBe('spawn /bin/nope: not found')
    expect(failing.find('[data-testid="mcp-state"]').text()).toContain('下个会话起不加载')

    // An editor gets the standing of the entry too, not the viewer's extras.
    const editing = mountDrawer({ mode: 'edit', row: row({ running: true, authStatus: 'needs-auth', officialUrl: true }) })
    expect(editing.find('[data-testid="mcp-state-status"]').text()).toBe('已连接')
    expect(editing.find('[data-testid="mcp-state-oauth"]').exists()).toBe(false)
    expect(editing.find('[data-testid="mcp-state-official"]').exists()).toBe(false)
  })

  it('says which agent overrides the shared entry it is showing', () => {
    // The prototype's `row.over`: the badge is the plain 未运行 and the grey
    // words beside it name the layer that wins, so a reader is not left asking
    // why an entry that is configured does nothing.
    const wrapper = mountDrawer({
      mode: 'view',
      row: row({ shadowedBy: 'agent', shadowedByName: 'main', running: false }),
    })

    expect(wrapper.find('[data-testid="mcp-state-status"]').text()).toBe('未运行')
    const hint = wrapper.find('[data-testid="mcp-state-shadowed"]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toBe('被主代理 main 覆盖')

    // An entry nothing overrides carries no such line.
    const plain = mountDrawer({ mode: 'view', row: row({ running: false }) })
    expect(plain.find('[data-testid="mcp-state-shadowed"]').exists()).toBe(false)
  })
})
