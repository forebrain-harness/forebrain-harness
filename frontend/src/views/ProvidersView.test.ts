import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import tip from '@/directives/tip'
import { forebrainApi, type ModelRecord, type ProviderLoginStatus, type ProviderRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import { openSelect, optionTexts, pickOption, selectedText } from '@/test-utils/formControls'
import { InlineTeleport } from '@/test-utils/portal'
import ProvidersView from './ProvidersView.vue'

// Every wrapper this file mounts goes away with its test. Left mounted, one
// test's page keeps re-rendering while the next test's mount installs its own
// Teleport stub — the portal then swaps node types mid-life and unmounts a
// teleport that never rendered — and its dialogs stay in `document.body`,
// where the next test's document-wide queries would find them.
enableAutoUnmount(afterEach)

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
    expect(primary.find('[data-testid="provider-key-saved-0"]').text()).toBe('••••3f9a')

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
 * The add/edit form: the API format and the provider's request params sit in the
 * form itself, the format is picked from a list instead of typed, and the params
 * are a JSON editor whose text has to be an object before the modal can save —
 * the server refuses anything else, and by then the modal would be gone.
 *
 * CodeMirror cannot lay out in jsdom, so the editor is replaced with a textarea
 * carrying the same v-model; what it renders is the e2e suite's business.
 */
vi.mock('@/components/providers/ParamsJsonField.vue', () => ({
  // `__esModule` is what the async loader looks at before it unwraps `default`;
  // without it the module namespace itself is handed to Vue as the component.
  __esModule: true,
  default: defineComponent({
    name: 'ParamsJsonField',
    props: {
      modelValue: { type: String, default: '' },
      invalid: { type: Boolean, default: false },
      ariaLabel: { type: String, default: '' },
    },
    emits: ['update:modelValue'],
    setup(props, { emit }) {
      return () =>
        h('textarea', {
          'data-testid': 'provider-params-editor',
          value: props.modelValue,
          onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLTextAreaElement).value),
        })
    },
  }),
}))

async function mountModalPage(): Promise<VueWrapper> {
  // Attached to the body: the modal teleports there, so its fields are only in
  // the document when the page itself is.
  const wrapper = mount(ProvidersView, { global: { directives: { tip } }, attachTo: document.body })
  await flushPromises()
  return wrapper
}

/** The modal teleports to the body, so its fields are read from the document. */
function field<T extends HTMLElement>(testid: string): T {
  const el = document.querySelector<T>(`[data-testid="${testid}"]`)
  if (!el) throw new Error(`no [data-testid="${testid}"] in the document`)
  return el
}

async function click(testid: string): Promise<void> {
  field(testid).click()
  await flushPromises()
}

async function type(testid: string, value: string): Promise<void> {
  const el = field<HTMLInputElement | HTMLTextAreaElement>(testid)
  el.value = value
  el.dispatchEvent(new Event('input'))
  await flushPromises()
}

describe('ProvidersView add/edit form', () => {
  beforeAll(() => {
    setLocale('zh')
    // The shadcn select measures its viewport with ResizeObserver, which jsdom
    // does not have; the refusal block is scrolled into view, and jsdom does
    // not implement that either.
    ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    Element.prototype.scrollIntoView = () => undefined
  })

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'providersPage').mockResolvedValue({ rows: [provider()], total: 1, page: 1, pageSize: 10 })
    vi.spyOn(forebrainApi, 'providers').mockResolvedValue([provider()])
    vi.spyOn(forebrainApi, 'saveProviders').mockResolvedValue({ applied: true })
    vi.spyOn(forebrainApi, 'getModels').mockResolvedValue({
      records: [loginModel('moonshotai', 'kimi-k3'), loginModel('anthropic', 'claude-sonnet-5')],
      status: [],
      providerBaseUrls: { moonshotai: 'https://api.moonshot.ai/v1' },
    })
  })

  afterEach(() => {
    // The page itself is unmounted by enableAutoUnmount (top of this file);
    // wiping the body here first would tear the nodes out from under that
    // unmount.
    vi.restoreAllMocks()
  })

  it('flattens the format and the params into the form, with no 高级 fold left', async () => {
    await mountModalPage()
    await click('providers-add')

    expect(document.querySelector('.forebrain-modal details')).toBeNull()
    // The key's storage is the gateway's business, not a line in the form.
    expect(document.body.textContent).not.toContain('配置文件里只写引用')
    expect(selectedText(field('provider-api-format'))).toBe('Chat Completions（/chat/completions）')
    await openSelect(field('provider-api-format'))
    expect(optionTexts()).toEqual([
      'Anthropic Messages（/v1/messages）',
      'Chat Completions（/chat/completions）',
      'Responses（/responses）',
    ])
    expect(field('provider-params-editor')).toBeTruthy()
  })

  it('picks the provider from a list and fills the endpoint it is served at', async () => {
    await mountModalPage()
    await click('providers-add')

    const providerSelect = field<HTMLElement>('provider-name')
    expect(providerSelect.getAttribute('role')).toBe('combobox')
    expect(selectedText(field('provider-name'))).toBe('选择服务商')
    await openSelect(field('provider-name'))
    expect(optionTexts()).toContain('moonshotai')

    await pickOption(field('provider-name'), 'moonshotai')
    expect(field<HTMLInputElement>('provider-base-url').value).toBe('https://api.moonshot.ai/v1')
    // A Chat Completions service starts on the empty path that names it.
    expect(selectedText(field('provider-api-format'))).toBe('Chat Completions（/chat/completions）')
  })

  it('says Anthropic Messages for the providers that speak it', async () => {
    // The engine builds the Messages client from the provider name whatever
    // api_path holds, so the form must not claim Chat Completions for it.
    vi.mocked(forebrainApi.providersPage).mockResolvedValue({
      rows: [provider({ provider: 'anthropic' })],
      total: 1,
      page: 1,
      pageSize: 10,
    })
    await mountModalPage()
    await click('provider-edit-0')

    expect(selectedText(field('provider-api-format'))).toBe('Anthropic Messages（/v1/messages）')

    // Picking one back also selects it, so switching providers cannot leave a
    // format behind that the new provider does not speak.
    await pickOption(field('provider-name'), 'moonshotai')
    expect(selectedText(field('provider-api-format'))).toBe('Chat Completions（/chat/completions）')
    await pickOption(field('provider-name'), 'anthropic')
    expect(selectedText(field('provider-api-format'))).toBe('Anthropic Messages（/v1/messages）')
  })

  it('keeps a provider the catalog no longer lists when a row is edited', async () => {
    vi.mocked(forebrainApi.providersPage).mockResolvedValue({
      rows: [provider({ provider: 'arcee-ai' })],
      total: 1,
      page: 1,
      pageSize: 10,
    })
    await mountModalPage()
    await click('provider-edit-0')

    expect(selectedText(field('provider-name'))).toBe('arcee-ai')
    await openSelect(field('provider-name'))
    expect(optionTexts()).toContain('arcee-ai')
  })

  it('saves the picked format as the row api_path', async () => {
    await mountModalPage()
    await click('providers-add')

    await pickOption(field('provider-name'), 'moonshotai')
    await type('provider-base-url', 'https://api.openai.com/v1')
    // A new key-mode row cannot be saved without its key (owner 2026-10-21).
    await type('provider-key-input', 'sk-typed-1234')
    await type('model-chips-input', 'gpt-5')
    field('model-chips-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    await pickOption(field('provider-api-format'), 'Responses（/responses）')
    await click('provider-save')

    const saved = vi.mocked(forebrainApi.saveProviders).mock.calls[0][0]
    expect(saved.map((entry) => entry.provider)).toEqual(['deepseek', 'moonshotai'])
    expect(saved[1].api_path).toBe('/responses')
  })

  it('keeps a path the list does not know instead of dropping it on the next edit', async () => {
    // A compatible endpoint serving its chat API under a path of its own: the
    // dropdown has no name for it, but losing it would change the row's
    // behaviour without anyone asking.
    vi.mocked(forebrainApi.providersPage).mockResolvedValue({
      rows: [provider({ apiPath: '/v1/chat/completions' })],
      total: 1,
      page: 1,
      pageSize: 10,
    })
    vi.mocked(forebrainApi.providers).mockResolvedValue([provider({ apiPath: '/v1/chat/completions' })])
    await mountModalPage()
    await click('provider-edit-0')

    const format = field<HTMLElement>('provider-api-format')
    expect(selectedText(field('provider-api-format'))).toBe('自定义（/v1/chat/completions）')
    await openSelect(field('provider-api-format'))
    expect(optionTexts()).toContain('自定义（/v1/chat/completions）')
    expect(format.getAttribute('role')).toBe('combobox')

    await type('provider-base-url', 'https://compatible.example.test/v1')
    await click('provider-save')

    const saved = vi.mocked(forebrainApi.saveProviders).mock.calls[0][0]
    expect(saved[0].api_path).toBe('/v1/chat/completions')
  })

  it('refuses params that are not a JSON object, and says why', async () => {
    await mountModalPage()
    await click('providers-add')

    await type('provider-params-editor', '{"temperature":')
    expect(field('provider-params-error').textContent?.trim()).toBe('JSON 格式有误')
    expect(field<HTMLButtonElement>('provider-save').disabled).toBe(true)

    await type('provider-params-editor', '[]')
    expect(field('provider-params-error').textContent?.trim()).toBe('参数必须是 JSON 对象')

    await type('provider-params-editor', '{"temperature":0.2}')
    expect(document.querySelector('[data-testid="provider-params-error"]')).toBeNull()
    expect(field<HTMLButtonElement>('provider-save').disabled).toBe(false)
  })

  it('will not save a new key-mode service without an api key, and says why', async () => {
    await mountModalPage()
    await click('providers-add')
    await pickOption(field('provider-name'), 'moonshotai')

    // A key-mode row with no stored key and nothing typed can never be
    // accepted, so the form refuses to send it and names the field.
    expect(field<HTMLButtonElement>('provider-save').disabled).toBe(true)
    expect(field('provider-key-hint').textContent?.trim()).toBe('这个模型服务用 API Key 认证，必须填写')

    await type('provider-key-input', 'sk-typed-1234')
    expect(field<HTMLButtonElement>('provider-save').disabled).toBe(false)
    expect(document.querySelector('[data-testid="provider-key-hint"]')).toBeNull()
  })

  it('does not ask for a key on the services that sign in instead', async () => {
    vi.mocked(forebrainApi.getModels).mockResolvedValue({
      records: [loginModel('chatgpt', 'gpt-5')],
      status: [],
    })
    await mountModalPage()
    await click('providers-add')
    await pickOption(field('provider-name'), 'chatgpt')

    // The credential is the login file; an empty key is this row's normal
    // shape, so the form saves it.
    expect(field<HTMLButtonElement>('provider-save').disabled).toBe(false)
    expect(document.querySelector('[data-testid="provider-key-hint"]')).toBeNull()
  })

  it('reports a refused save inside the dialog instead of behind the mask', async () => {
    // A message belongs to the surface that raised it (owner 2026-10-21): the
    // dialog stays open on a refusal, and its page banner sits under the mask
    // the dialog itself paints — so the reason is painted by the dialog.
    const sentence = 'moonshotai needs api_key: this model service authenticates with a key, not a login'
    await mountModalPage()
    await click('provider-edit-0')
    await click('provider-key-change')
    vi.mocked(forebrainApi.saveProviders).mockRejectedValue({ response: { status: 400, data: sentence } })

    await click('provider-save')

    expect(field('provider-save-error').textContent).toContain(sentence)
    expect(document.querySelector('.forebrain-modal')).not.toBeNull()
    // Exactly once: a second copy would be the page banner, which must no
    // longer carry a refusal the dialog owns.
    expect(document.body.textContent?.split(sentence)).toHaveLength(2)
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
  const wrapper = mount(ProvidersView, { global: { directives: { tip }, stubs: { teleport: InlineTeleport } } })
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

    // One way in: the Go device flow starts with the modal, with no choice of
    // plans in front of it.
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
