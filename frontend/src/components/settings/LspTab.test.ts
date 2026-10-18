import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import {
  lspInstall,
  lspPage,
  lspResetRecommendations,
  lspSetEnabled,
  type LspServerStatus,
  type LspSnapshotPage,
} from '@/lib/api'
import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import LspTab from './LspTab.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    lspPage: vi.fn(),
    lspSetEnabled: vi.fn(),
    lspRestart: vi.fn(),
    lspInstall: vi.fn(),
    lspResetRecommendations: vi.fn(),
  }
})

vi.mock('@/composables/useLastSession', () => ({
  lastSessionIdValue: () => 'session-1',
}))

/**
 * The settings page's language-server tab (plan 076): the search on the left of
 * a toolbar, a fixed table whose action column is icons only, the pager under
 * it and the page's own notes below that. What a row cannot say — the binary,
 * the problems, the log path, the last error — opens in a read-only drawer.
 */
function server(overrides: Partial<LspServerStatus> = {}): LspServerStatus {
  return {
    id: 'gopls',
    languages: ['Go'],
    role: 'primary',
    scope: 'catalog',
    enabled: true,
    state: 'ready',
    binaryPath: '/usr/local/bin/gopls',
    version: 'v0.20.0',
    errors: 2,
    logPath: '/state/lsp/gopls-ab12cd34.log',
    projectWrites: ['.gopls'],
    ...overrides,
  }
}

function pageOf(servers: LspServerStatus[], overrides: Partial<LspSnapshotPage> = {}): LspSnapshotPage {
  return {
    rows: servers,
    total: servers.length,
    page: 1,
    pageSize: 10,
    projectRoot: '/Users/tester/proj',
    trusted: true,
    featureEnabled: true,
    toolRegistered: true,
    recommendationsDisabled: false,
    projectNotes: [],
    ...overrides,
  }
}

async function mountTab(): Promise<VueWrapper> {
  const wrapper = mount(LspTab, { global: { directives: { tip }, stubs: { teleport: true } } })
  await flushPromises()
  return wrapper
}

describe('LspTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.clearAllMocks()
    vi.mocked(lspPage).mockResolvedValue(
      pageOf([server(), server({ id: 'pyright', languages: ['Python'], enabled: false, state: 'not_installed', installCommand: 'npm install -g pyright', binaryPath: undefined, version: undefined, logPath: undefined, projectWrites: undefined })]),
    )
    vi.mocked(lspSetEnabled).mockResolvedValue({ ok: true })
    vi.mocked(lspInstall).mockResolvedValue({ ok: true, started: true })
    vi.mocked(lspResetRecommendations).mockResolvedValue({ ok: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('opens on the search toolbar, the table, the pager and the note — no title row', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('h2').exists()).toBe(false)
    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('input[type="search"]').attributes('placeholder')).toBe('搜索语言服务')
    expect(toolbar.find('input[type="search"]').classes()).toContain('h-[34px]')
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    // The page's description and the project facts it used to keep in a heading
    // now sit under the table, in the tab's one note.
    const note = wrapper.find('[data-testid="lsp-note"]')
    expect(note.text()).toContain('本会话所属 Runner 的语言服务器')
    expect(note.find('[data-testid="lsp-project-line"]').text()).toContain('/Users/tester/proj')
    // The action column is icons only: the words are the tips on them.
    const actions = wrapper.findAll('.forebrain-row-action')
    expect(actions.length).toBeGreaterThan(0)
    for (const action of actions) expect(action.text()).toBe('')
    expect(wrapper.find('[data-detail="gopls"]').exists()).toBe(true)
  })

  it('shows each server as a row: the id, its languages, its scope and its state', async () => {
    const wrapper = await mountTab()

    const row = wrapper.find('[data-server="gopls"]')
    expect(row.text()).toContain('gopls')
    expect(row.text()).toContain('Go')
    expect(row.find('.scope-badge').text()).toContain('内置目录')
    expect(row.find('[data-state="ready"]').text()).toBe('已就绪')
    expect(row.text()).toContain('/usr/local/bin/gopls · v0.20.0')
    expect(wrapper.find('[data-server="pyright"] [data-state="not_installed"]').text()).toBe('未安装')
  })

  it('searches on the server, back at the first page', async () => {
    const wrapper = await mountTab()
    expect(lspPage).toHaveBeenCalledTimes(1)
    expect(vi.mocked(lspPage).mock.calls[0][0]).toMatchObject({ q: '', page: 1, sessionId: 'session-1' })

    vi.useFakeTimers()
    await wrapper.find('[data-testid="lsp-search"]').setValue('py')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()

    expect(lspPage).toHaveBeenCalledTimes(2)
    expect(vi.mocked(lspPage).mock.calls[1][0]).toMatchObject({ q: 'py', page: 1 })
  })

  it('enables a server through the control plane', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-enable="pyright"]').trigger('click')
    await flushPromises()
    expect(lspSetEnabled).toHaveBeenCalledWith('pyright', true, 'session-1')
  })

  it('starts an install only after the full command was confirmed', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-install="pyright"]').trigger('click')
    // The dialog shows the whole command before anything runs.
    const confirm = wrapper.find('[data-testid="lsp-install-confirm"]')
    expect(confirm.exists()).toBe(true)
    expect(confirm.text()).toContain('npm install -g pyright')
    expect(lspInstall).not.toHaveBeenCalled()

    await wrapper.find('[data-install-cancel="pyright"]').trigger('click')
    expect(lspInstall).not.toHaveBeenCalled()

    await wrapper.find('[data-install="pyright"]').trigger('click')
    await wrapper.find('[data-install-run="pyright"]').trigger('click')
    await flushPromises()
    expect(lspInstall).toHaveBeenCalledTimes(1)
    expect(lspInstall).toHaveBeenCalledWith('pyright', 'session-1')
  })

  it('opens one server read-only, with its log path and its last error', async () => {
    vi.mocked(lspPage).mockResolvedValue(pageOf([server({ lastError: 'gopls: no such file' })]))
    const wrapper = await mountTab()

    expect(wrapper.find('.forebrain-drawer').exists()).toBe(false)
    await wrapper.find('[data-detail="gopls"]').trigger('click')

    const drawer = wrapper.find('.forebrain-drawer')
    expect(drawer.text()).toContain('/usr/local/bin/gopls')
    expect(drawer.text()).toContain('v0.20.0')
    expect(drawer.text()).toContain('2 个错误')
    expect(drawer.text()).toContain('/state/lsp/gopls-ab12cd34.log')
    expect(drawer.find('[data-copy="gopls"]').exists()).toBe(true)
    expect(drawer.find('[data-field="lsp-error"]').text()).toBe('gopls: no such file')
  })

  it('polls while a server installs and stops once none is', async () => {
    vi.useFakeTimers()
    const installing = pageOf([server({ id: 'pyright', enabled: false, state: 'not_installed', installing: true })])
    const settled = pageOf([server({ id: 'pyright', enabled: false, state: 'not_installed' })])
    vi.mocked(lspPage)
      .mockResolvedValueOnce(installing) // mount
      .mockResolvedValueOnce(installing) // first tick: still installing
      .mockResolvedValue(settled) // second tick: the install ended

    const wrapper = mount(LspTab, { global: { directives: { tip }, stubs: { teleport: true } } })
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(lspPage).toHaveBeenCalledTimes(1)

    // Still installing: the next refresh comes two seconds later.
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(lspPage).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-server="pyright"] [data-state="installing"]').exists()).toBe(true)

    // The install finished: no further refresh fires.
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(lspPage).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(4000)
    await flushPromises()
    expect(lspPage).toHaveBeenCalledTimes(3)
  })

  it('turns recommendations back on from the note', async () => {
    vi.mocked(lspPage).mockResolvedValue(pageOf([server()], { recommendationsDisabled: true, recommendationsDisabledReason: 'dismissed 5 times' }))
    const wrapper = await mountTab()

    const section = wrapper.find('[data-testid="lsp-recommendations"]')
    expect(section.text()).toContain('dismissed 5 times')
    await section.find('button').trigger('click')
    await flushPromises()
    expect(lspResetRecommendations).toHaveBeenCalledWith('session-1')
  })

  it('surfaces a failed read as the error it is', async () => {
    vi.mocked(lspPage).mockRejectedValueOnce(new Error('language servers are not available'))
    const wrapper = await mountTab()
    expect(wrapper.find('[data-testid="lsp-error"]').text()).toContain('language servers are not available')
  })
})

describe('LspTab in English', () => {
  beforeEach(() => {
    setLocale('en')
    vi.clearAllMocks()
    vi.mocked(lspPage).mockResolvedValue(pageOf([server()]))
  })

  afterEach(() => {
    setLocale('zh')
    vi.restoreAllMocks()
  })

  it('names the columns and the actions in the viewer\'s language', async () => {
    const wrapper = await mountTab()
    const heads = wrapper.findAll('thead th').map((th) => th.text())
    expect(heads).toEqual(['Language server', 'Scope', 'State', 'Program', 'Actions'])
    expect(wrapper.find('[data-detail="gopls"]').attributes('aria-label')).toBe('Details gopls')
  })
})
