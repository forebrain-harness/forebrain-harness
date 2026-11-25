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
    {
      id: 'volcengine',
      name: '火山方舟',
      flows: ['device_code'],
      state: 'usable',
      account: { name: 'AKLT…f3a9', detail: 'STS' },
      canLogout: true,
    },
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

  // The account the stored sign-in names takes the first step's second line:
  // the person is choosing between accounts, not between protocols, and a
  // subscription with no account keeps the flow sentence it always had.
  it('names the account beside a signed-in subscription on the first step', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    const wrapper = mountModal()
    await flushPromises()

    expect(wrapper.find('[data-testid="models-sub-account-volcengine"]').text()).toBe('AKLT…f3a9 · STS')
    expect(wrapper.find('[data-testid="models-sub-account-chatgpt"]').exists()).toBe(false)
  })

  // A usable row opens straight on its models with the account bar above them:
  // who the machine is signed in as, and the way out of it.
  it('shows the account bar with a sign-out on the models step', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine' })])

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()

    const bar = wrapper.find('[data-testid="models-login-account"]')
    expect(bar.exists()).toBe(true)
    expect(bar.text()).toContain('AKLT…f3a9')
    expect(bar.text()).toContain('STS')
    expect(wrapper.find('[data-testid="models-logout"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="models-switch-account"]').exists()).toBe(true)
  })

  // A confirmed sign-out removes the login, tells the page that owns the table
  // to refresh, and lands the person back on the first step.
  it('signs out through the confirmation and returns to the first step', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine' })])
    const logout = vi.spyOn(forebrainApi, 'logoutSubscription').mockResolvedValue(undefined)

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="models-logout"]').trigger('click')
    await flushPromises()

    // The confirmation says what a sign-out costs before anything is removed.
    expect(logout).not.toHaveBeenCalled()
    const message = wrapper.find('.forebrain-confirm-message').text()
    expect(message).toContain('将删除本机保存的登录凭据')
    expect(message).toContain('已添加的模型会保留，但在重新登录前无法调用')

    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()

    expect(logout).toHaveBeenCalledWith('volcengine')
    expect(wrapper.emitted('changed')).toBeTruthy()
    expect(wrapper.text()).toContain('第 1 步')
  })

  // A shared credential file is named in the confirmation: the sign-out would
  // remove the Codex CLI's own login, and the person decides knowing it.
  it('names the shared credential file in the sign-out confirmation', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue([
      { ...subscriptions()[2], account: { name: 'dev@example.test', sharedPath: '/Users/dev/.codex/auth.json' } },
    ])
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine' })])
    vi.spyOn(forebrainApi, 'logoutSubscription').mockResolvedValue(undefined)

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="models-logout"]').trigger('click')
    await flushPromises()

    const message = wrapper.find('.forebrain-confirm-message').text()
    expect(message).toContain('/Users/dev/.codex/auth.json')
    expect(message).toContain('这份凭据与 Codex CLI 共用')
    expect(message).toContain('退出会同时让 Codex CLI 退出登录')
    // Nothing was removed while the confirmation stood open.
    expect(vi.mocked(forebrainApi.logoutSubscription)).not.toHaveBeenCalled()
  })

  // An external source answering 409 is told as it is: the stored login was
  // removed, but the machine will be signed in again. The page reads the
  // refusal's data, never its text.
  it('explains a 409 external login after the sign-out', async () => {
    const rows = subscriptions()
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(rows)
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine' })])
    vi.spyOn(forebrainApi, 'logoutSubscription').mockRejectedValueOnce(
      new GatewayHttpError(409, JSON.stringify({ error: 'an external source provides this sign-in', external_login: true })),
    )

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="models-logout"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="models-logout-external"]').text()).toContain('本机保存的登录已删除')
    expect(wrapper.find('[data-testid="models-logout-external"]').text()).toContain('重新登录')
    expect(wrapper.emitted('changed')).toBeTruthy()
  })

  // A sign-in the environment provides is named but never removable here: no
  // sign-out and no switch, only the hint that says where it comes from.
  it('offers no sign-out for a sign-in the environment provides', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue([
      {
        id: 'opencode-go',
        name: 'OpenCode Go',
        flows: ['device_code'],
        state: 'usable',
        account: { name: 'OPENCODE_API_KEY', envKey: true },
        canLogout: false,
      },
    ])
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer()])

    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.find('[data-testid="models-sub-account-opencode-go"]').text()).toContain('环境变量提供')
    await wrapper.find('[data-subscription="opencode-go"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="models-logout"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="models-switch-account"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="models-login-envkey"]').text()).toContain('OPENCODE_API_KEY')
    expect(wrapper.find('[data-testid="models-login-envkey"]').text()).toContain('unset')
  })

  // Switching accounts runs the flow again on purpose: the gateway's
  // "already signed in" shortcut is exactly what must not fire.
  it('switching accounts starts the flow with force', async () => {
    vi.spyOn(forebrainApi, 'subscriptions').mockResolvedValue(subscriptions())
    vi.spyOn(forebrainApi, 'subscriptionOffers').mockResolvedValue([offer({ provider: 'volcengine' })])
    const start = vi
      .spyOn(forebrainApi, 'startProviderLogin')
      .mockResolvedValue({ provider: 'volcengine', loginId: 'login-9' })
    vi.spyOn(forebrainApi, 'providerLoginStatus').mockResolvedValue(
      status({ status: 'complete', baseUrl: 'https://opencode.ai/zen/go/v1', flow: 'device_code' }),
    )

    const wrapper = mountModal()
    await flushPromises()
    await wrapper.find('[data-subscription="volcengine"]').trigger('click')
    await flushPromises()
    expect(start).not.toHaveBeenCalled()

    await wrapper.find('[data-testid="models-switch-account"]').trigger('click')
    await flushPromises()
    expect(start).toHaveBeenCalledWith('volcengine', { force: true })
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
