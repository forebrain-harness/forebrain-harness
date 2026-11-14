import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import tip from '@/directives/tip'
import forebrainApi, {
  GatewayHttpError,
  type ProviderLoginStatus,
  type SubscriptionOffer,
  type SubscriptionRow,
} from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'

import SubscriptionLoginModal from './SubscriptionLoginModal.vue'

/**
 * The one door into every subscription (plan 266): pick it, sign in to it, choose
 * the models it unlocked. The three branches that matter are the two flow kinds
 * (a device code to type, an address to paste back) and the offers step, whose
 * key-question lives under the plan it concerns.
 */

beforeAll(() => setLocale('zh'))
afterEach(() => vi.restoreAllMocks())

function subscriptions(): SubscriptionRow[] {
  return [
    { id: 'opencode-go', name: 'OpenCode Go', flows: ['device_code'], state: 'missing' },
    { id: 'chatgpt', name: 'ChatGPT', flows: ['paste_callback'], state: 'missing' },
    { id: 'volcengine', name: '火山方舟', flows: ['device_code'], state: 'usable' },
  ]
}

function offer(overrides: Partial<SubscriptionOffer> = {}): SubscriptionOffer {
  return {
    plan: 'Go 订阅',
    planName: 'Go 订阅',
    provider: 'opencode-go',
    model: 'glm-5.3',
    baseUrl: 'https://opencode.ai/zen/go/v1',
    apiPath: '/chat/completions',
    endpoint: 'https://opencode.ai/zen/go/v1/chat/completions',
    contextWindow: 1_000_000,
    supportsImages: false,
    default: false,
    held: false,
    fromStaticList: false,
    needsKey: false,
    ...overrides,
  }
}

function mountModal(): VueWrapper {
  return mount(SubscriptionLoginModal, {
    props: { open: true },
    attachTo: document.body,
    global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
}

function status(overrides: Partial<ProviderLoginStatus> = {}): ProviderLoginStatus {
  return { status: 'pending', url: '', userCode: '', error: '', baseUrl: '', flow: '', ...overrides }
}

describe('SubscriptionLoginModal', () => {
  it('lists the subscriptions that can sign in, with how each one stands', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    const wrapper = mountModal()
    await flushPromises()

    expect(wrapper.findAll('[data-subscription]')).toHaveLength(3)
    expect(wrapper.text()).toContain('第 1 步')
    expect(wrapper.find('[data-testid="models-sub-state-volcengine"]').text()).toBe('已登录')
    expect(wrapper.find('[data-testid="models-sub-state-chatgpt"]').text()).toBe('未登录')
  })

  it('renders a device code and advances to the models when the poll completes', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'startProviderLogin').mockResolvedValue({ provider: 'opencode-go', loginId: 'login-1' })
    const channel: ProviderLoginStatus[] = [
      status({ url: 'https://opencode.ai/console/device?user_code=ABCD-1234', userCode: 'ABCD-1234', flow: 'device_code' }),
      status({ status: 'complete', baseUrl: 'https://opencode.ai/zen/go/v1', flow: 'device_code' }),
    ]
    vi.spyOn(forebrainApi, 'providerLoginStatus').mockImplementation(async () => channel.shift() ?? status({ status: 'complete' }))
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer()])

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="opencode-go"]').trigger('click')
    await flushPromises()

    // The flow's own kind decides the branch: a code, an authorization page, and
    // no paste box.
    // The page polls once a second, so the code arrives on the second tick.
    await vi.waitFor(() => expect(wrapper.find('[data-testid="models-login-code"]').text()).toBe('ABCD-1234'), {
      timeout: 5000,
    })
    expect(wrapper.find('[data-testid="models-login-paste"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('第 2 步')

    await vi.waitFor(() => expect(wrapper.text()).toContain('第 3 步'), { timeout: 5000 })
    expect(wrapper.find('[data-offer="0"]').exists()).toBe(true)
  })

  it('renders the paste branch for a callback that cannot reach the gateway', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'startProviderLogin').mockResolvedValue({ provider: 'chatgpt', loginId: 'login-2' })
    vi.spyOn(forebrainApi, 'providerLoginStatus').mockResolvedValue(
      status({ url: 'https://auth.openai.com/oauth/authorize', flow: 'paste_callback' }),
    )
    const submit = vi.spyOn(forebrainApi, 'submitProviderLoginCallback').mockResolvedValue(undefined)

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="chatgpt"]').trigger('click')
    await flushPromises()

    await vi.waitFor(() => expect(wrapper.find('[data-testid="models-login-paste"]').exists()).toBe(true))
    expect(wrapper.find('[data-testid="models-login-code"]').exists()).toBe(false)

    await wrapper.find('[data-testid="models-login-paste"]').setValue('http://localhost:1455/auth/callback?code=abc')
    await wrapper.find('[data-testid="models-login-paste-submit"]').trigger('click')
    await flushPromises()
    expect(submit).toHaveBeenCalledWith('chatgpt', 'login-2', 'http://localhost:1455/auth/callback?code=abc')
  })

  it('a signed-in subscription opens straight on its models', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    const start = vi.spyOn(forebrainApi, 'startProviderLogin')
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine', plan: 'Coding Plan' })])

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()

    // No flow to run: the models are the first thing on the screen.
    expect(wrapper.text()).toContain('第 3 步')
    expect(start).not.toHaveBeenCalled()
    expect(wrapper.find('[data-offer="0"]').exists()).toBe(true)
  })

  // What this install already holds is ticked, the way the terminal's /connect
  // opens on it: the vendor's own default is only the fallback for a first
  // sign-in.
  it('preselects the models this install already holds, not the vendor default', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([
      offer({ provider: 'volcengine', plan: 'Coding Plan', model: 'doubao-seed-2.1-pro', default: true }),
      offer({ provider: 'volcengine', plan: 'Coding Plan', model: 'kimi-k3', held: true }),
      offer({ provider: 'volcengine', plan: 'Coding Plan', model: 'qwen3.8-max', held: true }),
    ])

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()

    const boxes = wrapper.findAll('input[type="checkbox"][data-offer]')
    expect(boxes.map((box) => (box.element as HTMLInputElement).checked)).toEqual([false, true, true])
  })

  it('asks which key when the plan holds several, and creates one when asked', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine', plan: 'Coding Plan' })])
    const provision = vi
      .spyOn(forebrainApi, 'provisionSubscription')
      .mockRejectedValueOnce(
        new GatewayHttpError(
          409,
          JSON.stringify({
            error: '这个套餐里有多把 Key，请选择一把',
            key_choice: [{ id: 'key-1', name: 'api-key', tail: 'd410' }],
          }),
        ),
      )
      .mockResolvedValueOnce({ ids: ['row-1'] })

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()

    await wrapper.find('[data-offer="0"]').trigger('click')
    await wrapper.find('[data-testid="models-provision"]').trigger('click')
    await flushPromises()

    // The refusal draws the key row under the plan it belongs to.
    expect(wrapper.find('[data-testid="models-key-choice"]').exists()).toBe(true)
    expect(provision).toHaveBeenCalledTimes(1)

    await wrapper.find('[data-testid="models-provision"]').trigger('click')
    await flushPromises()
    expect(provision.mock.calls[1][1]).toMatchObject({ keyId: 'key-1' })
    expect(wrapper.emitted('saved')).toBeTruthy()
  })

  // plan 283: the third step's rows carry a second check box — may the agent
  // pick this model for a subagent on its own? It is off unless ticked, and it
  // travels with the offer it belongs to.
  it('sends the subagent authorization each ticked model carries', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([
      offer({ provider: 'volcengine', plan: 'Coding Plan', model: 'doubao-a' }),
      offer({ provider: 'volcengine', plan: 'Coding Plan', model: 'doubao-b' }),
    ])
    vi.spyOn(forebrainApi, 'provisionSubscription').mockResolvedValue({ ids: ['row-1', 'row-2'] })

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()

    // Unticked model: its authorization cannot be ticked either.
    expect(wrapper.find('[data-offer-subagents="0"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-offer="0"]').trigger('click')
    await wrapper.find('[data-offer="1"]').trigger('click')
    await wrapper.find('[data-offer-subagents="1"]').trigger('click')
    await wrapper.find('[data-testid="models-provision"]').trigger('click')
    await flushPromises()

    const body = vi.mocked(forebrainApi.provisionSubscription).mock.calls[0][1] as {
      offers: { model: string; subagentAllowed: boolean }[]
    }
    expect(body.offers.map((o) => [o.model, o.subagentAllowed])).toEqual([
      ['doubao-a', false],
      ['doubao-b', true],
    ])
  })

  // plan 281: a plan whose key nobody can fetch asks for it on the page — a
  // masked field named for the plan, sent with the write — and the add button
  // stays shut until it is there.
  it('asks for the key a plan cannot fetch and sends it with the write', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([
      offer({ provider: 'volcengine', plan: 'coding-plan', planName: 'Coding Plan', needsKey: true }),
    ])
    vi.spyOn(forebrainApi, 'provisionSubscription').mockResolvedValue({ ids: ['row-1'] })

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-offer="0"]').trigger('click')
    await flushPromises()

    const field = wrapper.find('[data-testid="models-key-paste"]')
    expect(field.exists()).toBe(true)
    expect(field.attributes('type')).toBe('password')
    // Nothing is written before the key is pasted.
    expect(wrapper.find('[data-testid="models-provision"]').attributes('disabled')).toBeDefined()

    await field.setValue('ark-pasted-placeholder')
    await wrapper.find('[data-testid="models-provision"]').trigger('click')
    await flushPromises()

    const body = vi.mocked(forebrainApi.provisionSubscription).mock.calls[0][1] as Record<string, unknown>
    expect(body.key).toBe('ark-pasted-placeholder')
  })

  // The refusal is the other way the field appears: a write that answered
  // "needs_key" shows it, and the plan's console comes with the answer.
  it('reveals the key field when the write says a plan needs one', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine', plan: 'coding-plan' })])
    vi.spyOn(forebrainApi, 'provisionSubscription')
      .mockRejectedValueOnce(
        new GatewayHttpError(
          409,
          JSON.stringify({
            error: '这个套餐的 Key 需要你从控制台复制后填进来',
            needs_key: true,
            plan: 'coding-plan',
            console_url: 'https://console.example.test/apiKey',
          }),
        ),
      )
      .mockResolvedValueOnce({ ids: ['row-1'] })

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-offer="0"]').trigger('click')
    await wrapper.find('[data-testid="models-provision"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="models-key-paste"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('https://console.example.test/apiKey')

    await wrapper.find('[data-testid="models-key-paste"]').setValue('ark-pasted-placeholder')
    await wrapper.find('[data-testid="models-provision"]').trigger('click')
    await flushPromises()
    const second = vi.mocked(forebrainApi.provisionSubscription).mock.calls[1][1] as Record<string, unknown>
    expect(second.key).toBe('ark-pasted-placeholder')
  })

  it('offers to create a key when the account holds none', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine', plan: 'Coding Plan' })])
    vi.spyOn(forebrainApi, 'provisionSubscription')
      .mockRejectedValueOnce(
        new GatewayHttpError(409, JSON.stringify({ error: '还没有 Key', needs_key_consent: true })),
      )
      .mockResolvedValueOnce({ ids: ['row-1'] })

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-offer="0"]').trigger('click')
    await wrapper.find('[data-testid="models-provision"]').trigger('click')
    await flushPromises()

    await wrapper.find('[data-testid="models-key-consent"]').trigger('click')
    await wrapper.find('[data-testid="models-provision"]').trigger('click')
    await flushPromises()

    const second = vi.mocked(forebrainApi.provisionSubscription).mock.calls[1][1] as Record<string, unknown>
    expect(second.createKey).toBe(true)
  })
})

// A subscription a local plugin owns carries its own wording and may run a
// browser-confirm device flow with no short code: Step 1 shows the plugin's
// description, and Step 2 shows its waiting hint, the open-authorization
// button, and no empty code placeholder.
const pluginRegistry = vi.hoisted(() => new Map<string, { id: string } & Record<string, unknown>>())
vi.mock('@/plugins/subscriptionPlugin', () => ({
  getSubscriptionFrontendPlugin: (id: string) => pluginRegistry.get(id.trim().toLowerCase()),
}))

describe('SubscriptionLoginModal plugin subscriptions', () => {
  it('shows the plugin wording and drops the empty code box for a codeless device flow', async () => {
    pluginRegistry.set('plugtest', {
      id: 'plugtest',
      describe: (locale: 'zh' | 'en') => (locale === 'zh' ? '浏览器授权登录 PLUGIN 订阅' : 'Authorize the PLUGIN subscription'),
      waitingHint: (locale: 'zh' | 'en') => (locale === 'zh' ? '在浏览器确认后自动继续-PLUGIN' : 'Confirms in the browser-PLUGIN'),
      hideEmptyUserCode: true,
    })
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue([
      ...subscriptions(),
      { id: 'plugtest', name: 'Plugin Sub', flows: ['device_code'], state: 'missing' },
    ])
    vi.spyOn(forebrainApi, 'startProviderLogin').mockResolvedValue({ provider: 'plugtest', loginId: 'login-p' })
    vi.spyOn(forebrainApi, 'providerLoginStatus').mockResolvedValue(
      status({ url: 'https://plugin.example.test/device/selectAccounts', userCode: '', flow: 'device_code' }),
    )
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'plugtest' })])

    const wrapper = mountModal()
    await flushPromises()

    // Step 1: the plugin's own description under the subscription's name.
    expect(wrapper.find('[data-subscription="plugtest"]').text()).toContain('浏览器授权登录 PLUGIN 订阅')

    await wrapper.find('[data-subscription="plugtest"]').trigger('click')
    await flushPromises()

    // Step 2: the plugin's waiting hint, no empty code placeholder for a flow
    // that has no code, and a usable open button once the URL arrives.
    await vi.waitFor(() => expect(wrapper.text()).toContain('在浏览器确认后自动继续-PLUGIN'), { timeout: 5000 })
    expect(wrapper.find('[data-testid="models-login-code"]').exists()).toBe(false)
    await vi.waitFor(
      () => expect(wrapper.find('[data-testid="models-login-open"]').attributes('disabled')).toBeUndefined(),
      { timeout: 5000 },
    )
  })
})
