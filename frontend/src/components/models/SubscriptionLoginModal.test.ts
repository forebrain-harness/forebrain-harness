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
    fromStaticList: false,
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
