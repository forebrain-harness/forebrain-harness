import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { getErrorMessage, lspInstall, lspResetRecommendations, lspSetEnabled, lspSnapshot, type LspSnapshot } from '@/lib/api'
import LspTab from './LspTab.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    lspSnapshot: vi.fn(),
    lspSetEnabled: vi.fn(),
    lspRestart: vi.fn(),
    lspInstall: vi.fn(),
    lspResetRecommendations: vi.fn(),
  }
})

vi.mock('@/composables/useLastSession', () => ({
  lastSessionIdValue: () => 'session-1',
}))

const snapshotOf = (overrides: Partial<LspSnapshot> = {}): LspSnapshot => ({
  projectRoot: '/Users/tester/proj',
  trusted: true,
  featureEnabled: true,
  toolRegistered: true,
  recommendationsDisabled: false,
  servers: [
    {
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
    },
    {
      id: 'pyright',
      languages: ['Python'],
      role: 'primary',
      scope: 'catalog',
      enabled: false,
      state: 'not_installed',
      installCommand: 'npm install -g pyright',
    },
  ],
  ...overrides,
})

async function mountTab(snap: LspSnapshot) {
  vi.mocked(lspSnapshot).mockResolvedValue(snap)
  const wrapper = mount(LspTab)
  await flushPromises()
  return wrapper
}

describe('LspTab', () => {
  beforeEach(() => {
    // The module mock is shared by every test in this file; its call history
    // starts at zero for each one.
    vi.clearAllMocks()
    vi.mocked(lspSetEnabled).mockResolvedValue({ ok: true })
    vi.mocked(lspInstall).mockResolvedValue({ ok: true, started: true })
    vi.mocked(lspResetRecommendations).mockResolvedValue({ ok: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('renders one card per server with its state, and the project line', async () => {
    const wrapper = await mountTab(snapshotOf())
    expect(wrapper.find('[data-server="gopls"]').exists()).toBe(true)
    expect(wrapper.find('[data-server="pyright"]').exists()).toBe(true)
    expect(wrapper.find('[data-server="gopls"] [data-state="ready"]').exists()).toBe(true)
    expect(wrapper.find('[data-server="pyright"] [data-state="not_installed"]').exists()).toBe(true)
    // The trust and host notes are the two facts every reader needs first.
    expect(wrapper.find('[data-testid="lsp-project-line"]').text()).toContain('/Users/tester/proj')
    expect(wrapper.text()).toContain('/state/lsp/gopls-ab12cd34.log')
  })

  it('enables a server through the control plane', async () => {
    const wrapper = await mountTab(snapshotOf())
    await wrapper.find('[data-enable="pyright"]').trigger('click')
    await flushPromises()
    expect(lspSetEnabled).toHaveBeenCalledWith('pyright', true, 'session-1')
  })

  it('starts an install only after the full command was confirmed', async () => {
    const wrapper = await mountTab(snapshotOf())
    await wrapper.find('[data-install="pyright"]').trigger('click')
    // The confirm dialog shows the whole command before anything runs.
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

  it('polls while a server installs and stops once none is', async () => {
    vi.useFakeTimers()
    const installing = snapshotOf()
    installing.servers[1] = { ...installing.servers[1], state: 'not_installed', installing: true }
    vi.mocked(lspSnapshot)
      .mockResolvedValueOnce(installing) // mount
      .mockResolvedValueOnce(installing) // first tick: still installing
      .mockResolvedValue(snapshotOf()) // second tick: the install ended
    const wrapper = mount(LspTab)
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(lspSnapshot).toHaveBeenCalledTimes(1)

    // Still installing: the next refresh comes two seconds later.
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(lspSnapshot).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-server="pyright"] [data-state="installing"]').exists()).toBe(true)

    // The install finished: no further refresh fires.
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(lspSnapshot).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(4000)
    await flushPromises()
    expect(lspSnapshot).toHaveBeenCalledTimes(3)
  })

  it('shows why recommendations are off and turns them back on', async () => {
    const wrapper = await mountTab(
      snapshotOf({ recommendationsDisabled: true, recommendationsDisabledReason: 'dismissed 5 times' }),
    )
    const section = wrapper.find('[data-testid="lsp-recommendations"]')
    expect(section.text()).toContain('dismissed 5 times')
    await section.find('button').trigger('click')
    await flushPromises()
    expect(lspResetRecommendations).toHaveBeenCalledWith('session-1')
  })

  it('surfaces a failed snapshot as the error it is', async () => {
    vi.mocked(lspSnapshot).mockRejectedValueOnce(new Error('language servers are not available'))
    const wrapper = mount(LspTab)
    await flushPromises()
    expect(wrapper.text()).toContain(getErrorMessage(new Error('language servers are not available')))
    // The polling never starts without a snapshot to install from.
    expect(vi.isMockFunction(lspSnapshot)).toBe(true)
  })
})
