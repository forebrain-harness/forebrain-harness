import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type ProviderRecord } from '@/lib/api'
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
