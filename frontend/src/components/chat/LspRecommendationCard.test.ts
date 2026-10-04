import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { decideLspRecommendation, lspSnapshot, type LspSnapshot } from '@/lib/api'
import { setLocale } from '@/locales'
import LspRecommendationCard from './LspRecommendationCard.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    decideLspRecommendation: vi.fn(),
    lspSnapshot: vi.fn(),
  }
})

const enableRec = {
  id: 'lsprec-1',
  serverId: 'gopls',
  displayName: 'gopls',
  languages: ['Go'],
  triggerExtension: '.go',
  mode: 'enable' as const,
  binaryPath: '/usr/local/bin/gopls',
  version: 'v0.23.0',
}

const emptySnapshot = (): LspSnapshot => ({
  trusted: true,
  featureEnabled: true,
  toolRegistered: true,
  recommendationsDisabled: false,
  servers: [],
})

describe('LspRecommendationCard', () => {
  beforeAll(() => setLocale('en'))
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it.each([
    ['enable', 'enable', 'gopls enabled for Go. Diagnostics start with the next edit.'],
    ['not_now', 'not_now', 'Not now. No language server will be suggested again in this session.'],
    ['never', 'never', 'gopls will not be suggested again. /lsp can still enable it.'],
    ['disable_all', 'disable_all', 'Language server recommendations are off. Turn them back on in /lsp.'],
  ])('the %s button answers %s and shows the result line', async (testid, choice, line) => {
    vi.useFakeTimers()
    vi.mocked(decideLspRecommendation).mockResolvedValue({ ok: true })
    vi.mocked(lspSnapshot).mockResolvedValue(emptySnapshot())
    const wrapper = mount(LspRecommendationCard, { props: { recommendation: enableRec, sessionId: 's1' } })
    await wrapper.find(`[data-choice="${testid}"]`).trigger('click')
    await flushPromises()

    expect(decideLspRecommendation).toHaveBeenCalledWith('lsprec-1', choice, 's1')
    const result = wrapper.find('[data-testid="lsp-rec-result"]')
    expect(result.exists()).toBe(true)
    expect(result.text()).toBe(line)

    // The line lingers, then the card closes itself.
    expect(wrapper.emitted('close')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(5100)
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('the close button answers not_now', async () => {
    vi.mocked(decideLspRecommendation).mockResolvedValue({ ok: true })
    vi.mocked(lspSnapshot).mockResolvedValue(emptySnapshot())
    const wrapper = mount(LspRecommendationCard, { props: { recommendation: enableRec } })
    await wrapper.find('[data-testid="lsp-rec-close"]').trigger('click')
    await flushPromises()
    expect(decideLspRecommendation).toHaveBeenCalledWith('lsprec-1', 'not_now', undefined)
    expect(wrapper.find('[data-testid="lsp-rec-result"]').text()).toContain('Not now.')
    wrapper.unmount()
  })

  it('names the fifth dismissal that turned recommendations off', async () => {
    vi.mocked(decideLspRecommendation).mockResolvedValue({ ok: true })
    vi.mocked(lspSnapshot).mockResolvedValue({ ...emptySnapshot(), recommendationsDisabled: true })
    const wrapper = mount(LspRecommendationCard, { props: { recommendation: enableRec } })
    await wrapper.find('[data-choice="not_now"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="lsp-rec-result"]').text())
      .toBe('Language server recommendations are now off (dismissed 5 times in a row). Turn them back on in /lsp.')
    wrapper.unmount()
  })

  it('keeps the buttons when the decision fails', async () => {
    vi.mocked(decideLspRecommendation).mockRejectedValue(new Error('choice refused'))
    const wrapper = mount(LspRecommendationCard, { props: { recommendation: enableRec } })
    await wrapper.find('[data-choice="enable"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="lsp-rec-error"]').text()).toContain('choice refused')
    expect(wrapper.find('[data-choice="enable"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('polls the install until it finishes, then shows the enabled line', async () => {
    vi.useFakeTimers()
    vi.mocked(decideLspRecommendation).mockResolvedValue({ ok: true })
    const installRec = {
      id: 'lsprec-2',
      serverId: 'pyright',
      displayName: 'pyright',
      languages: ['Python'],
      triggerExtension: '.py',
      mode: 'install' as const,
      installCommand: 'npm install -g pyright',
    }
    vi.mocked(lspSnapshot)
      .mockResolvedValueOnce({ ...emptySnapshot(), servers: [{ id: 'pyright', role: 'primary', scope: 'catalog', enabled: false, state: 'not_installed', installing: true }] })
      .mockResolvedValueOnce({ ...emptySnapshot(), servers: [{ id: 'pyright', role: 'primary', scope: 'catalog', enabled: false, state: 'not_installed', installing: true }] })
      .mockResolvedValueOnce({
        ...emptySnapshot(),
        servers: [{ id: 'pyright', role: 'primary', scope: 'catalog', enabled: true, state: 'available', installing: false }],
      })
    const wrapper = mount(LspRecommendationCard, { props: { recommendation: installRec, sessionId: 's1' } })
    await wrapper.find('[data-choice="install"]').trigger('click')
    await flushPromises()
    expect(decideLspRecommendation).toHaveBeenCalledWith('lsprec-2', 'install', 's1')
    expect(wrapper.find('[data-testid="lsp-rec-installing"]').text()).toBe('Installing…')

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(wrapper.find('[data-testid="lsp-rec-installing"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    const result = wrapper.find('[data-testid="lsp-rec-result"]')
    expect(result.exists()).toBe(true)
    expect(result.text()).toBe('pyright installed and enabled for Python. Diagnostics start with the next edit.')
    wrapper.unmount()
  })

  it('reports a failed install from the snapshot', async () => {
    vi.useFakeTimers()
    vi.mocked(decideLspRecommendation).mockResolvedValue({ ok: true })
    const installRec = {
      id: 'lsprec-3',
      serverId: 'pyright',
      displayName: 'pyright',
      languages: ['Python'],
      triggerExtension: '.py',
      mode: 'install' as const,
      installCommand: 'npm install -g pyright',
    }
    vi.mocked(lspSnapshot).mockResolvedValue({
      ...emptySnapshot(),
      servers: [{ id: 'pyright', role: 'primary', scope: 'catalog', enabled: false, state: 'not_installed', installing: false, installError: 'exit status 1: no network' }],
    })
    const wrapper = mount(LspRecommendationCard, { props: { recommendation: installRec } })
    await wrapper.find('[data-choice="install"]').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(wrapper.find('[data-testid="lsp-rec-result"]').text()).toBe('Install failed: exit status 1: no network')
    wrapper.unmount()
  })
})
