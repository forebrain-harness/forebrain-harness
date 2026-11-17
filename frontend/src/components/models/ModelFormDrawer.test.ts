import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import tip from '@/directives/tip'
import forebrainApi, { GatewayHttpError, type LlmModelRow } from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'

import ModelCapsFields from './ModelCapsFields.vue'
import ModelFormDrawer from './ModelFormDrawer.vue'

/**
 * The add/edit drawer (plan 266). These hold the promises the form makes about
 * the two things a wrong guess would break: the stored API key (sent back only
 * when the person changed it) and the four identity fields (a duplicate names
 * the row that has them, and the drawer offers to open it).
 *
 * The capability lookup and the provider registry are asked over the network, so
 * every case stubs them; the editor is CodeMirror, which cannot lay out in jsdom
 * and is stubbed the way the old page's test did.
 */

vi.mock('@/components/models/ParamsJsonField.vue', () => ({
  default: {
    name: 'ParamsJsonField',
    props: ['modelValue'],
    template: '<textarea class="params-stub" :value="modelValue" />',
  },
}))

// jsdom has no layout, so `Element.prototype.scrollIntoView` does not exist;
// reka's listbox (the provider and model menus) calls it when the highlighted
// item changes, and the missing method surfaces as an unhandled rejection that
// fails the suite's exit code. A browser has it, so the shim is the environment's
// gap and not the component's behaviour.
if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}

beforeAll(() => setLocale('zh'))
afterEach(() => vi.restoreAllMocks())

const PROVIDERS = [
  {
    id: 'deepseek',
    name: '深度求索',
    loginBacked: false,
    endpoints: [{ name: '标准', baseUrl: 'https://api.deepseek.com', apiPath: '/chat/completions' }],
  },
  { id: 'custom', name: '自定义', loginBacked: false, endpoints: [] },
]

const PROTOCOLS = [
  { apiPath: '/chat/completions', name: 'OpenAI Chat Completions' },
  { apiPath: '/responses', name: 'OpenAI Responses' },
  { apiPath: '/v1/messages', name: 'Anthropic Messages' },
]

function row(overrides: Partial<LlmModelRow> = {}): LlmModelRow {
  return {
    id: 'row-1',
    provider: 'deepseek',
    providerName: '深度求索',
    custom: false,
    model: 'deepseek-v4-pro',
    baseUrl: 'https://api.deepseek.com',
    apiPath: '/chat/completions',
    urlVerbatim: false,
    subagentAllowed: false,
    protocol: 'chat_completions',
    endpoint: 'https://api.deepseek.com/chat/completions',
    apiKeySet: true,
    apiKeyLast4: '9876',
    apiKey: 'sk-stored-key-9876',
    loginBacked: false,
    caps: {},
    effortsKnown: false,
    catalogCaps: {},
    isDefault: false,
    createdAt: 0,
    ...overrides,
  }
}

function mountDrawer(props: Record<string, unknown> = {}): VueWrapper {
  return mount(ModelFormDrawer, {
    props: { open: true, mode: 'edit', row: row(), providers: PROVIDERS, protocols: PROTOCOLS, customId: 'custom', ...props },
    attachTo: document.body,
    global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
}

describe('ModelFormDrawer', () => {
  beforeAll(() => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockResolvedValue({ effortsKnown: false })
  })

  it('titles itself by what opened it', async () => {
    const editing = mountDrawer()
    await flushPromises()
    expect(editing.find('.forebrain-drawer-head b').text()).toBe('编辑模型')
    editing.unmount()

    const adding = mountDrawer({ mode: 'add', row: null })
    await flushPromises()
    expect(adding.find('.forebrain-drawer-head b').text()).toBe('添加模型')
  })

  it('an edit whose key nobody touched sends no api_key at all', async () => {
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer()
    await flushPromises()

    // The box carries the stored key — the eye can really show it — and saving
    // without typing sends the identity and the capabilities, never the key.
    const key = wrapper.find('[data-testid="models-api-key"]')
    expect((key.element as HTMLInputElement).value).toBe('sk-stored-key-9876')
    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()

    expect(update).toHaveBeenCalledTimes(1)
    const body = update.mock.calls[0][1] as Record<string, unknown>
    expect(body).not.toHaveProperty('apiKey')
    expect(body).toMatchObject({ provider: 'deepseek', model: 'deepseek-v4-pro', apiPath: '/chat/completions' })
  })

  it('a typed key replaces the stored one', async () => {
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer()
    await flushPromises()

    await wrapper.find('[data-testid="models-api-key"]').setValue('sk-typed-1111')
    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()

    expect((update.mock.calls[0][1] as Record<string, unknown>).apiKey).toBe('sk-typed-1111')
  })

  it('reveals a stored key through the eye, and hides it again', async () => {
    const wrapper = mountDrawer()
    await flushPromises()

    const key = () => wrapper.find('[data-testid="models-api-key"]').element as HTMLInputElement
    expect(key().type).toBe('password')

    await wrapper.find('[data-reveal]').trigger('click')
    expect(key().type).toBe('text')
    expect(key().value).toBe('sk-stored-key-9876')
  })

  it('a refused create names the row that already has the four fields', async () => {
    const conflict = new GatewayHttpError(
      409,
      JSON.stringify({ error: '已经有一条相同的配置', existing_id: 'row-existing' }),
    )
    vi.spyOn(forebrainApi, 'createLlmModel').mockRejectedValue(conflict)
    const wrapper = mountDrawer({ mode: 'add', row: null })
    await flushPromises()

    await wrapper.find('[data-testid="models-provider-trigger"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-provider="deepseek"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="models-api-key"]').setValue('sk-e2e-new')
    await wrapper.find('[data-testid="models-model-name"]').setValue('deepseek-v4-pro')
    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="models-duplicate"]').exists()).toBe(true)
    // 「去编辑那一条」 carries the id the refusal named, not the form's own values.
    await wrapper.find('[data-testid="models-goto-duplicate"]').trigger('click')
    expect(wrapper.emitted('duplicate')?.[0]).toEqual(['row-existing'])
  })

  it('cannot save a custom model that names no protocol', async () => {
    const wrapper = mountDrawer({ mode: 'add', row: null })
    await flushPromises()

    await wrapper.find('[data-testid="models-provider-trigger"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-provider="custom"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="models-model-name"]').setValue('qwen3-coder')

    expect(wrapper.find('[data-testid="models-save"]').attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="models-protocol-trigger"]').trigger('click')
    await wrapper.find('[data-protocol="/responses"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="models-save"]').attributes('disabled')).toBeUndefined()
  })

  it('a subscription row offers only its capabilities for editing', async () => {
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer({
      row: row({
        source: 'volcengine',
        sourceName: '火山方舟',
        sourcePlan: 'Coding Plan 个人版',
        sourceState: 'usable',
        model: 'doubao-seed-2.1-pro',
        baseUrl: 'https://ark.cn-beijing.volces.com/api/coding',
        apiPath: '/v1/messages',
      }),
    })
    await flushPromises()

    // The identity is the sign-in's: no provider picker, no address field, and
    // the source card says where the row came from.
    expect(wrapper.find('[data-testid="models-source-card"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="models-provider-trigger"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Coding Plan 个人版')

    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()

    const body = update.mock.calls[0][1] as Record<string, unknown>
    // The authorization switch is a permission, not the row's identity, so it
    // travels with the advanced settings the sign-in row does allow (plan 283).
    expect(Object.keys(body).sort()).toEqual(['caps', 'params', 'subagentAllowed'])
    expect(body.subagentAllowed).toBe(false)
  })
})

// The subagent authorization (plan 283) is the drawer's own switch under the
// model name: it opens on the row's stored answer, a new row opens closed, and
// the answer travels with the save — also for a row a sign-in wrote.
describe('ModelFormDrawer catalog provider', () => {
  beforeAll(() => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockResolvedValue({ effortsKnown: false })
  })

  it('offers the provider field only for a custom model', async () => {
    const builtIn = mountDrawer()
    await flushPromises()
    expect(builtIn.find('[data-testid="models-catalog-provider"]').exists()).toBe(false)
    builtIn.unmount()

    const custom = mountDrawer({
      mode: 'add',
      row: null,
      presetProvider: 'custom',
    })
    await flushPromises()
    expect(custom.find('[data-testid="models-catalog-provider"]').exists()).toBe(true)
    expect(custom.text()).toContain('数据服务商（可选）')
  })

  it('fills the stored provider and sends it back with the row', async () => {
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer({
      row: row({
        provider: 'custom',
        custom: true,
        model: 'qwen4-max-preview',
        catalogProvider: 'alibaba',
        catalogId: 'alibaba/qwen4-max-preview',
        catalogCaps: { contextWindow: 1000000 },
      }),
    })
    await flushPromises()

    const field = wrapper.find('[data-testid="models-catalog-provider"]')
    expect((field.element as HTMLInputElement).value).toBe('alibaba')
    // The matched entry and its window, in the terminal's compact spelling.
    const matched = wrapper.find('[data-testid="models-catalog-matched"]')
    expect(matched.exists()).toBe(true)
    expect(matched.text()).toBe('官方数据：alibaba/qwen4-max-preview 1M 窗口')
    expect(wrapper.find('[data-testid="models-catalog-missing"]').exists()).toBe(false)

    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()
    expect(update).toHaveBeenCalledTimes(1)
    const body = update.mock.calls[0][1] as Record<string, unknown>
    expect(body.catalogProvider).toBe('alibaba')
  })

  it('says so when nothing describes the model name', async () => {
    const wrapper = mountDrawer({
      row: row({ provider: 'custom', custom: true, model: 'nothing-at-all', catalogProvider: '', catalogId: '' }),
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="models-catalog-matched"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="models-catalog-missing"]').text()).toContain('官方数据里没有这个模型')
  })
})

describe('ModelFormDrawer subagent authorization', () => {
  it('opens on the stored answer and saves the change', async () => {
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row({ subagentAllowed: true }))
    const wrapper = mountDrawer({ row: row({ subagentAllowed: true }) })
    await flushPromises()

    expect(wrapper.find('[data-testid="models-subagent-allowed"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('主代理可用它派子代理')
    expect(wrapper.text()).toContain('关闭时，只有你亲自选择才会用它')

    await wrapper.get('[data-testid="models-save"]').trigger('click')
    await flushPromises()
    const body = update.mock.calls[0][1] as Record<string, unknown>
    expect(body.subagentAllowed).toBe(true)
  })

  it('starts a new row closed', async () => {
    const create = vi.spyOn(forebrainApi, 'createLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer({ mode: 'create' })
    await flushPromises()

    await wrapper.get('[data-testid="models-provider-trigger"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-provider="deepseek"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="models-model-name"]').setValue('deepseek-v4-pro')
    await wrapper.get('[data-testid="models-save"]').trigger('click')
    await flushPromises()

    const body = (create.mock.calls[0]?.[0] ?? {}) as Record<string, unknown>
    expect(body.subagentAllowed).toBe(false)
  })
})

// The default thinking level shares the TUI's three-state rule (plan 311): the
// dropdown offers what the model can be given — `none` among them only where the
// model can turn thinking off *and* the protocol can send it — a stored value the
// list no longer holds is reset rather than left to be refused, and the switch
// beside it stays the capability it has always been.
describe('ModelFormDrawer default effort', () => {
  const OFF_LABEL = '关闭思考'

  async function openAdvanced(wrapper: VueWrapper): Promise<void> {
    await wrapper.find('[data-testid="models-advanced-toggle"]').trigger('click')
    await flushPromises()
  }

  it('offers 关闭思考 where the model has the switch on', async () => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockResolvedValue({
      effortsKnown: true,
      efforts: ['low', 'medium'],
      reasoningOff: true,
    })
    const wrapper = mountDrawer({
      row: row({ caps: { reasoningOff: true }, effortsKnown: true, catalogCaps: { efforts: ['low', 'medium'], reasoningOff: true } }),
    })
    await flushPromises()
    await openAdvanced(wrapper)

    const fields = wrapper.findComponent(ModelCapsFields)
    expect(fields.props('options')).toEqual(['none', 'low', 'medium'])
    expect(OFF_LABEL).toBeTruthy()
    wrapper.unmount()
  })

  it('leaves it out where the model cannot turn thinking off', async () => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockResolvedValue({ effortsKnown: true, efforts: ['low', 'high'] })
    const wrapper = mountDrawer({
      row: row({ effortsKnown: true, catalogCaps: { efforts: ['low', 'high'] } }),
    })
    await flushPromises()
    await openAdvanced(wrapper)

    expect(wrapper.findComponent(ModelCapsFields).props('options')).toEqual(['low', 'high'])
    wrapper.unmount()
  })

  it('withdrawing 允许关闭思考 removes it and resets a stored 关闭思考', async () => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockResolvedValue({
      effortsKnown: true,
      efforts: ['none', 'low', 'high'],
      reasoningOff: true,
    })
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer({
      row: row({
        effortsKnown: true,
        catalogCaps: { efforts: ['none', 'low', 'high'], reasoningOff: true },
        caps: { reasoningOff: true, defaultEffort: 'none' },
      }),
    })
    await flushPromises()
    await openAdvanced(wrapper)
    expect(wrapper.findComponent(ModelCapsFields).props('options')).toEqual(['none', 'low', 'high'])

    // The person says the model cannot turn thinking off after all: an explicit
    // no is not overruled by the catalog, and the stored 关闭思考 is no longer a
    // value the list offers.
    await wrapper.find('[data-switch="reasoningOff"]').trigger('click')
    await flushPromises()
    expect(wrapper.findComponent(ModelCapsFields).props('options')).toEqual(['low', 'high'])

    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()
    const body = update.mock.calls[0][1] as Record<string, unknown>
    expect((body.caps as Record<string, unknown>).defaultEffort).toBeUndefined()
    expect((body.caps as Record<string, unknown>).reasoningOff).toBe(false)
    wrapper.unmount()
  })

  it('a protocol switch that makes the stored default illegal resets it', async () => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockImplementation(async () => ({
      effortsKnown: true,
      // The Messages answer: none is not sendable, so the dropdown's list
      // narrows and the stored default has no home.
      efforts: ['low', 'medium'],
    }))
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer({
      row: row({ caps: { defaultEffort: 'none' }, catalogCaps: { efforts: ['none', 'low', 'medium'], reasoningOff: true } }),
    })
    // The capability lookup is debounced, so the answer for this endpoint is
    // what the dropdown ends up holding.
    await new Promise((resolve) => setTimeout(resolve, 450))
    await flushPromises()
    await openAdvanced(wrapper)
    expect(wrapper.findComponent(ModelCapsFields).props('options')).toEqual(['low', 'medium'])
    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()
    const body = update.mock.calls[0][1] as Record<string, unknown>
    expect((body.caps as Record<string, unknown>).defaultEffort).toBeUndefined()
    wrapper.unmount()
  })

  it('saves 关闭思考 as default_effort none', async () => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockResolvedValue({
      effortsKnown: true,
      efforts: ['low', 'medium'],
      reasoningOff: true,
    })
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer({
      row: row({ caps: { reasoningOff: true } , catalogCaps: { efforts: ['low', 'medium'], reasoningOff: true } }),
    })
    await flushPromises()
    await openAdvanced(wrapper)

    wrapper.findComponent(ModelCapsFields).vm.$emit('update:defaultEffort', 'none')
    await flushPromises()
    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()

    const body = update.mock.calls[0][1] as Record<string, unknown>
    expect((body.caps as Record<string, unknown>).defaultEffort).toBe('none')
    wrapper.unmount()
  })

  it('saving 默认 omits default_effort and still sends the whole caps block', async () => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockResolvedValue({
      effortsKnown: true,
      efforts: ['low', 'medium'],
      reasoningOff: true,
    })
    const update = vi.spyOn(forebrainApi, 'updateLlmModel').mockResolvedValue(row())
    const wrapper = mountDrawer({
      row: row({ caps: { reasoningOff: true, defaultEffort: 'low' }, catalogCaps: { efforts: ['low', 'medium'], reasoningOff: true } }),
    })
    await flushPromises()
    await openAdvanced(wrapper)

    wrapper.findComponent(ModelCapsFields).vm.$emit('update:defaultEffort', '')
    await flushPromises()
    await wrapper.find('[data-testid="models-save"]').trigger('click')
    await flushPromises()

    const body = update.mock.calls[0][1] as Record<string, unknown>
    // The gateway replaces the whole caps block, so an omitted default is the
    // clearing (plan 311 §2.2): the block is still sent.
    expect(body).toHaveProperty('caps')
    expect((body.caps as Record<string, unknown>).defaultEffort).toBeUndefined()
    wrapper.unmount()
  })

  it('a row no source lists offers the levels it declares, trimmed', async () => {
    vi.spyOn(forebrainApi, 'llmModelCatalogCaps').mockResolvedValue({
      effortsKnown: false,
      // The gateway answered from the row's own declaration, already trimmed to
      // what the protocol can send.
      efforts: ['low'],
    })
    const wrapper = mountDrawer({
      row: row({
        provider: 'custom',
        custom: true,
        apiPath: '/v1/messages',
        caps: { efforts: ['minimal', 'low'] },
        effortsKnown: false,
        catalogCaps: { efforts: ['low'] },
      }),
    })
    await flushPromises()
    await openAdvanced(wrapper)

    const fields = wrapper.findComponent(ModelCapsFields)
    expect(fields.props('options')).toEqual(['low'])
    // The checkboxes still replay the row's own declaration.
    expect(fields.props('draftEfforts')).toEqual(['minimal', 'low'])
    wrapper.unmount()
  })
})
