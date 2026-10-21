import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type ModelRecord, type ProviderLoginStatus, type ProviderRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ProvidersView from './ProvidersView.vue'

/**
 * The model services (plan 073): the fallback order paged by the server, each
 * row naming its place in the whole order, and 上移 swapping with the row
 * before it there — the write is whole-table, so it reads the order back.
 */
function provider(overrides: Partial<ProviderRecord> = {}): ProviderRecord {
  return {
    provider: 'deepseek',
    models: ['deepseek-v4'],
    baseUrl: 'https://api.deepseek.com',
    apiPath: '',
    apiKeySet: true,
    apiKeyHint: '3f9a',
    index: 0,
    ...overrides,
  }
}

async function mountPage(): Promise<VueWrapper> {
  const wrapper = mount(ProvidersView, { global: { directives: { tip } } })
  await flushPromises()
  return wrapper
}
describe('ProvidersView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'providersPage').mockResolvedValue({
      rows: [provider(), provider({ provider: 'openai', models: ['gpt-5', 'gpt-5-mini'], baseUrl: 'https://api.openai.com/v1', apiKeySet: false, apiKeyHint: '', index: 1 })],
      total: 2,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'providers').mockResolvedValue([
      provider(),
      provider({ provider: 'openai', models: ['gpt-5', 'gpt-5-mini'], baseUrl: 'https://api.openai.com/v1', apiKeySet: false, apiKeyHint: '', index: 1 }),
    ])
    vi.spyOn(forebrainApi, 'saveProviders').mockResolvedValue({ applied: true })
    vi.spyOn(forebrainApi, 'getModels').mockResolvedValue({ records: [], status: [] })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('opens on a search toolbar, the table, the pager and the note — no title row', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('h2').exists()).toBe(false)

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-provider-row]').length).toBe(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe('按顺序尝试：第一条主用，其余回退。')
  })

  it('draws the position, the models, the endpoint and whether a key is stored', async () => {
    const wrapper = await mountPage()

    const primary = wrapper.find('[data-provider-row="deepseek"]')
    expect(primary.find('.forebrain-badge').text()).toBe('主用')
    expect(primary.text()).toContain('deepseek-v4')
    expect(primary.text()).toContain('https://api.deepseek.com')
    expect(primary.find('[data-testid="provider-key-saved-0"]').text()).toBe('已保存 ••••3f9a')

    const fallback = wrapper.find('[data-provider-row="openai"]')
    expect(fallback.text()).toContain('回退 1')
    expect(fallback.text()).toContain('gpt-5、gpt-5-mini')
    expect(fallback.find('[data-testid="provider-key-unset-1"]').text()).toBe('未设置')
  })

  it('leaves the first row without 上移 and names the row on every other action', async () => {
    const wrapper = await mountPage()

    const primary = wrapper.find('[data-provider-row="deepseek"]')
    expect(primary.find('.forebrain-icon-slot').exists()).toBe(true)
    expect(primary.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual([
      '编辑 deepseek',
      '删除 deepseek',
    ])

    const fallback = wrapper.find('[data-provider-row="openai"]')
    expect(fallback.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual([
      '上移 openai',
      '编辑 openai',
      '删除 openai',
    ])
  })

  it('moves a row up in the whole order, not on the page', async () => {
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="provider-up-1"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.providers).toHaveBeenCalled()
    const saved = vi.mocked(forebrainApi.saveProviders).mock.calls[0][0]
    expect(saved.map((entry) => entry.provider)).toEqual(['openai', 'deepseek'])
    // The untouched key does not travel: only a typed one is sent.
    expect(saved[1].api_key_plain).toBeUndefined()
  })

  it('moves a row up once per click, even when the clicks arrive together', async () => {
    // The row's place in the order is not its identity: a write pins the entry
    // by its own fields and the writes run one after another, so two quick
    // clicks carry the row two places up instead of undoing each other.
    const order = ['alpha', 'beta', 'gamma']
    const row = (name: string, index: number) => provider({ provider: name, models: [`${name}-model`], index })
    vi.mocked(forebrainApi.providersPage).mockResolvedValue({
      rows: order.map((name, index) => row(name, index)),
      total: order.length,
      page: 1,
      pageSize: 10,
    })
    vi.mocked(forebrainApi.providers).mockImplementation(async () => order.map((name, index) => row(name, index)))
    vi.mocked(forebrainApi.saveProviders).mockImplementation(async (entries) => {
      order.splice(0, order.length, ...entries.map((entry) => String(entry.provider)))
      return { applied: true }
    })
    const wrapper = await mountPage()

    const up = wrapper.find('[data-testid="provider-up-2"]')
    void up.trigger('click')
    void up.trigger('click')
    await flushPromises()
    await flushPromises()

    // Two clicks: gamma passes beta and then alpha.
    expect(order).toEqual(['gamma', 'alpha', 'beta'])
  })
})

/**
 * The two signed-in services (OpenCode, ChatGPT): the gateway runs the flow and
 * the page polls it, showing the device code or the authorization link, then
 * the catalog the login unlocked, then writing the chosen model into the same
 * ordered list. OpenCode's free tier skips the login entirely.
 */
function loginModel(provider: string, id: string, overrides: Partial<ModelRecord> = {}): ModelRecord {
  return { modelId: `${provider}/${id}`, modelName: id, provider, apiModel: id, ...overrides }
}

function loginStatus(overrides: Partial<ProviderLoginStatus> = {}): ProviderLoginStatus {
  return { status: 'pending', url: '', userCode: '', error: '', baseUrl: '', ...overrides }
}

/** The modal teleports to the body; stubbing the teleport keeps it inside the
 * wrapper, so the steps are found and clicked like anything else on the page. */
async function mountConnectPage(): Promise<VueWrapper> {
  const wrapper = mount(ProvidersView, { global: { directives: { tip }, stubs: { teleport: true } } })
  await flushPromises()
  return wrapper
}

/** One poll period of the gateway status endpoint, and the promises it settles. */
async function tick(): Promise<void> {
  await vi.advanceTimersByTimeAsync(1000)
  await flushPromises()
}

describe('ProvidersView sign-in', () => {
  beforeAll(() => setLocale('zh'))

  // jsdom has no scrollIntoView, and the catalog list scrolls the chosen model
  // into view; without the stub every one of these tests ends in an unhandled
  // rejection that fails the run.
  const scrollIntoView = Element.prototype.scrollIntoView

  beforeEach(() => {
    Element.prototype.scrollIntoView = () => undefined
    vi.spyOn(forebrainApi, 'providersPage').mockResolvedValue({
      rows: [provider()],
      total: 1,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'providers').mockResolvedValue([provider()])
    vi.spyOn(forebrainApi, 'saveProviders').mockResolvedValue({ applied: true })
    vi.spyOn(forebrainApi, 'getModels').mockResolvedValue({ records: [], status: [] })
    vi.spyOn(forebrainApi, 'startProviderLogin').mockResolvedValue({ loginId: 'login-1', provider: 'opencode' })
    vi.spyOn(forebrainApi, 'providerLoginStatus').mockResolvedValue(loginStatus())
    vi.spyOn(forebrainApi, 'cancelProviderLogin').mockResolvedValue(undefined)
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    Element.prototype.scrollIntoView = scrollIntoView
  })

  it('signs in to OpenCode and writes the Go subscription row it unlocks', async () => {
    // A root only the login's completion answer carries: the page's own
    // constants name opencode.ai, so a payload holding this one proves the base
    // URL was taken from the answer instead of from a copy of the gateway's.
    const answeredRoot = 'https://go.example.test/inference/go'
    vi.mocked(forebrainApi.startProviderLogin).mockResolvedValue({ loginId: 'login-1', provider: 'opencode' })
    vi.mocked(forebrainApi.providerLoginStatus)
      // The code appears a beat after the start.
      .mockResolvedValueOnce(loginStatus())
      .mockResolvedValueOnce(
        loginStatus({ url: 'https://opencode.ai/console/device?user_code=ABCD-1234', userCode: 'ABCD-1234' }),
      )
      .mockResolvedValue(loginStatus({ status: 'complete', baseUrl: answeredRoot }))
    vi.mocked(forebrainApi.getModels).mockResolvedValue({
      records: [loginModel('opencode-go', 'glm-5.3', { isDefault: true }), loginModel('opencode-go', 'kimi-k3')],
      status: [],
    })
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)

    const wrapper = await mountConnectPage()
    await wrapper.find('[data-testid="providers-connect-opencode"]').trigger('click')
    await flushPromises()

    // The free tier comes before anything starts a login.
    expect(wrapper.find('[data-testid="provider-login-free"]').exists()).toBe(true)
    expect(forebrainApi.startProviderLogin).not.toHaveBeenCalled()

    await wrapper.find('[data-testid="provider-login-start"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.startProviderLogin).toHaveBeenCalledWith('opencode')

    // The first poll is a moment after the start, before the code exists.
    await tick()
    expect(wrapper.find('[data-testid="provider-login-code"]').text()).toBe('正在获取…')

    await tick()
    expect(wrapper.find('[data-testid="provider-login-code"]').text()).toBe('ABCD-1234')

    // The authorization page is the flow's own URL, opened by the page.
    await wrapper.find('[data-testid="provider-login-open"]').trigger('click')
    expect(open).toHaveBeenCalledWith('https://opencode.ai/console/device?user_code=ABCD-1234', '_blank', 'noopener')

    await tick()
    expect(forebrainApi.getModels).toHaveBeenCalledWith({ provider: 'opencode-go' })
    expect(wrapper.find('[data-testid="provider-login-model-glm-5.3"]').exists()).toBe(true)

    await wrapper.find('[data-testid="provider-login-save"]').trigger('click')
    await flushPromises()

    const saved = vi.mocked(forebrainApi.saveProviders).mock.calls[0][0]
    expect(saved).toHaveLength(2)
    // The login unlocks the Go subscription's catalog, so the row is
    // "opencode-go", and its base URL is exactly what the completion answer
    // named — not a value the page keeps a copy of.
    expect(saved[1]).toMatchObject({
      provider: 'opencode-go',
      models: ['glm-5.3'],
      base_url: answeredRoot,
    })
    // A signed-in service has no typed key: the credential is the login's.
    expect(saved[1].api_key_plain).toBeUndefined()
    // Saving closes the modal.
    expect(wrapper.find('[data-testid="provider-login-save"]').exists()).toBe(false)
  })

  it('signs in to ChatGPT and writes the endpoint its completion named', async () => {
    vi.mocked(forebrainApi.startProviderLogin).mockResolvedValue({ loginId: 'gpt-1', provider: 'chatgpt' })
    vi.mocked(forebrainApi.providerLoginStatus)
      .mockResolvedValueOnce(loginStatus({ url: 'https://auth.openai.com/oauth/authorize?client_id=abc' }))
      .mockResolvedValue(
        loginStatus({ status: 'complete', baseUrl: 'https://chatgpt.com/backend-api/codex' }),
      )
    vi.mocked(forebrainApi.getModels).mockResolvedValue({
      records: [loginModel('chatgpt', 'gpt-5.6', { isDefault: true })],
      status: [],
    })

    const wrapper = await mountConnectPage()
    await wrapper.find('[data-testid="providers-connect-chatgpt"]').trigger('click')
    await flushPromises()

    // One way in, so the flow starts with the modal.
    expect(forebrainApi.startProviderLogin).toHaveBeenCalledWith('chatgpt')
    expect(wrapper.find('[data-testid="provider-login-free"]').exists()).toBe(false)

    await tick()
    expect(wrapper.find('[data-testid="provider-login-open"]').attributes('disabled')).toBeUndefined()

    await tick()
    expect(forebrainApi.getModels).toHaveBeenCalledWith({ provider: 'chatgpt' })
    await wrapper.find('[data-testid="provider-login-save"]').trigger('click')
    await flushPromises()

    const saved = vi.mocked(forebrainApi.saveProviders).mock.calls[0][0]
    expect(saved[1]).toMatchObject({
      provider: 'chatgpt',
      models: ['gpt-5.6'],
      base_url: 'https://chatgpt.com/backend-api/codex',
    })
  })

  it('takes the free tier without a login', async () => {
    vi.mocked(forebrainApi.getModels).mockResolvedValue({
      records: [loginModel('opencode', 'big-pickle', { modelName: 'big-pickle (free)', isDefault: true })],
      status: [],
    })

    const wrapper = await mountConnectPage()
    await wrapper.find('[data-testid="providers-connect-opencode"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="provider-login-free"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.startProviderLogin).not.toHaveBeenCalled()
    expect(forebrainApi.getModels).toHaveBeenCalledWith({ provider: 'opencode' })

    await wrapper.find('[data-testid="provider-login-save"]').trigger('click')
    await flushPromises()

    const saved = vi.mocked(forebrainApi.saveProviders).mock.calls[0][0]
    expect(saved[1]).toMatchObject({
      provider: 'opencode',
      models: ['big-pickle'],
      base_url: 'https://opencode.ai/inference',
    })
  })

  it('says why a sign-in ended and starts a fresh one on retry', async () => {
    vi.mocked(forebrainApi.providerLoginStatus).mockResolvedValue(loginStatus({ status: 'failed', error: 'denied' }))

    const wrapper = await mountConnectPage()
    await wrapper.find('[data-testid="providers-connect-chatgpt"]').trigger('click')
    await flushPromises()
    await tick()

    // The OAuth vocabulary is not shown as it is; the sentence says it.
    expect(wrapper.find('[data-testid="provider-login-error"]').text()).toBe('授权被拒绝，可以重试。')

    await wrapper.find('[data-testid="provider-login-retry"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.startProviderLogin).toHaveBeenCalledTimes(2)
  })

  it('offers a restart when the gateway no longer knows the login', async () => {
    vi.mocked(forebrainApi.providerLoginStatus).mockResolvedValue(null)

    const wrapper = await mountConnectPage()
    await wrapper.find('[data-testid="providers-connect-chatgpt"]').trigger('click')
    await flushPromises()
    await tick()

    expect(wrapper.find('[data-testid="provider-login-error"]').text()).toBe('这次登录已经失效。')
    expect(wrapper.find('[data-testid="provider-login-retry"]').exists()).toBe(true)
  })

  it('cancels the flow when the modal is closed and stops polling', async () => {
    const wrapper = await mountConnectPage()
    await wrapper.find('[data-testid="providers-connect-chatgpt"]').trigger('click')
    await flushPromises()
    await tick()
    expect(forebrainApi.providerLoginStatus).toHaveBeenCalledTimes(1)

    await wrapper.find('[data-testid="provider-login-cancel"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.cancelProviderLogin).toHaveBeenCalledWith('chatgpt', 'login-1')
    expect(wrapper.find('[data-testid="provider-login-code"]').exists()).toBe(false)

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()
    expect(forebrainApi.providerLoginStatus).toHaveBeenCalledTimes(1)
  })
})
