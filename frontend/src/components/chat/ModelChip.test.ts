import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { forebrainApi, type SessionModelOption } from '@/lib/api'
import { setLocale } from '@/locales'
import ModelChip from './ModelChip.vue'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

/**
 * The model menu's "accepts images" badge (the design's `.model-menu .badge.ok`)
 * and the entry point a refusal notice uses to open the menu.
 */

beforeAll(() => {
  setLocale('zh')
})
afterAll(async () => {
  await vi.dynamicImportSettled()
  setLocale('en')
})
afterEach(() => vi.restoreAllMocks())

const options: SessionModelOption[] = [
  { id: 'm-pro', provider: 'deepseek', model: 'deepseek-v4-pro', label: 'DeepSeek V4 Pro', efforts: ['low', 'medium', 'high', 'xhigh'], supportsImages: false },
  { id: 'm-flash', provider: 'deepseek', model: 'deepseek-v4-flash', label: 'DeepSeek V4 Flash', efforts: [], supportsImages: true },
]

function mountChip(props: Record<string, unknown> = {}) {
  return mount(ModelChip, {
    props: {
      sessionId: 's1',
      selection: { modelId: 'm-pro', provider: 'deepseek', model: 'deepseek-v4-pro', effort: '' },
      options,
      ...props,
    },
  })
}

describe('ModelChip', () => {
  it('marks only the models the catalog says accept images', async () => {
    const wrapper = mountChip()
    await wrapper.find('[data-testid="model-chip"]').trigger('click')

    const flash = wrapper.find('[data-testid="model-option-deepseek-v4-flash"]')
    expect(flash.text()).toContain('支持图片')
    expect(flash.find('[data-testid="model-supports-images"]').exists()).toBe(true)

    const pro = wrapper.find('[data-testid="model-option-deepseek-v4-pro"]')
    expect(pro.find('[data-testid="model-supports-images"]').exists()).toBe(false)
  })

  it('opens the menu when something else asks it to', async () => {
    const wrapper = mountChip()
    expect(wrapper.find('[data-testid="model-option-deepseek-v4-flash"]').exists()).toBe(false)

    ;(wrapper.vm as unknown as { openMenu: () => void }).openMenu()
    await wrapper.vm.$nextTick()

    expect(wrapper.find('[data-testid="model-option-deepseek-v4-flash"]').exists()).toBe(true)
  })

  it('stays shut when the chip is disabled', async () => {
    const wrapper = mountChip({ disabled: true })

    ;(wrapper.vm as unknown as { openMenu: () => void }).openMenu()
    await wrapper.vm.$nextTick()

    expect(wrapper.find('[data-testid="model-option-deepseek-v4-flash"]').exists()).toBe(false)
  })

  // With the subagent drawer open the composer keeps every control but drops
  // the leading glyph: the name alone still says which model is in force.
  it('drops its icon when the composer is squeezed', () => {
    const full = mountChip()
    const compact = mountChip({ compact: true })
    expect(full.get('[data-testid="model-chip"]').findAll('svg')).toHaveLength(2)
    expect(compact.get('[data-testid="model-chip"]').findAll('svg')).toHaveLength(1)
    expect(compact.get('[data-testid="model-chip"]').text()).toContain('deepseek-v4-pro')
  })

  // A new chat has no session row to write: the chip works and hands the pick
  // to the page, which writes it once the first message opens the session.
  it('hands a pick on a new chat to the page instead of writing a session row', async () => {
    const save = vi.spyOn(forebrainApi, 'saveSessionModel').mockResolvedValue({ provider: '', model: '', effort: '' })
    const wrapper = mountChip({ sessionId: '' })
    expect(wrapper.find('[data-testid="model-chip"]').attributes('disabled')).toBeUndefined()

    await wrapper.find('[data-testid="model-chip"]').trigger('click')
    await wrapper.find('[data-testid="model-option-deepseek-v4-flash"]').trigger('click')

    expect(save).not.toHaveBeenCalled()
    expect(wrapper.emitted('pick')?.[0]?.[0]).toMatchObject({ provider: 'deepseek', model: 'deepseek-v4-flash' })
    expect(wrapper.emitted('saved')).toBeUndefined()
  })

  // Past eight rows the menu is no longer scannable: the page puts a filter
  // above the list (plan 266), and typing in it narrows by model, provider or
  // address.
  it('offers a filter once the list is long, and narrows on what is typed', async () => {
    const many: SessionModelOption[] = Array.from({ length: 9 }, (_, i) => ({
      id: `row-${i}`,
      provider: 'deepseek',
      providerName: '深度求索',
      model: `deepseek-v4-${i}`,
      label: `deepseek/deepseek-v4-${i}`,
      baseUrl: `https://eu-${i}.test/v1`,
      apiPath: '/chat/completions',
      efforts: [],
    }))
    const wrapper = mountChip({ options: many, selection: { modelId: 'row-0', provider: 'deepseek', model: 'deepseek-v4-0', effort: '' } })
    await wrapper.find('[data-testid="model-chip"]').trigger('click')

    const filter = wrapper.find('[data-testid="model-filter"]')
    expect(filter.exists()).toBe(true)

    await filter.setValue('eu-7')
    const options = wrapper.findAll('[role="option"]')
    expect(options).toHaveLength(1)
    expect(options[0].text()).toContain('eu-7.test')
  })

  it('has no filter for a short list', async () => {
    const wrapper = mountChip()
    await wrapper.find('[data-testid="model-chip"]').trigger('click')

    expect(wrapper.find('[data-testid="model-filter"]').exists()).toBe(false)
  })

  // Each option is the model's whole address (plan 266): the name alone cannot
  // tell two configured rows apart.
  it('writes every option as the model’s signature', async () => {
    const twins: SessionModelOption[] = [
      { id: 'row-eu', provider: 'deepseek', providerName: '深度求索', model: 'deepseek-v4-flash', label: 'eu', baseUrl: 'https://eu.test/v1', apiPath: '/chat/completions', efforts: [] },
      { id: 'row-us', provider: 'deepseek', providerName: '深度求索', model: 'deepseek-v4-flash', label: 'us', baseUrl: 'https://us.test/v1', apiPath: '/chat/completions', efforts: [] },
    ]
    const wrapper = mountChip({ options: twins, selection: { modelId: 'row-eu', provider: 'deepseek', model: 'deepseek-v4-flash', effort: '' } })
    await wrapper.find('[data-testid="model-chip"]').trigger('click')

    const options = wrapper.findAll('[role="option"]')
    expect(options[0].text()).toContain('eu.test/v1')
    expect(options[0].find('.forebrain-path').text()).toBe('/chat/completions')
    expect(options[1].text()).toContain('us.test/v1')
  })

  // The same provider+model may be configured more than once (another base
  // url, api path or key). The row id is the identity: only the row the
  // selection names is current, and a pick sends that id — a pair alone cannot
  // tell the two apart, and the server refuses one it cannot tell apart.
  it('names the configured row, not just the pair, for twin models', async () => {
    const twins: SessionModelOption[] = [
      { id: 'row-eu', provider: 'deepseek', model: 'deepseek-v4-flash', label: 'deepseek/deepseek-v4-flash', baseUrl: 'https://eu.test/v1', apiPath: '/chat/completions', efforts: [] },
      { id: 'row-us', provider: 'deepseek', model: 'deepseek-v4-flash', label: 'deepseek/deepseek-v4-flash', baseUrl: 'https://us.test/v1', apiPath: '/chat/completions', efforts: [] },
    ]
    const save = vi.spyOn(forebrainApi, 'saveSessionModel').mockResolvedValue({ modelId: 'row-us', provider: 'deepseek', model: 'deepseek-v4-flash', effort: '' })
    const wrapper = mountChip({
      options: twins,
      selection: { modelId: 'row-us', provider: 'deepseek', model: 'deepseek-v4-flash', effort: '' },
    })
    await wrapper.find('[data-testid="model-chip"]').trigger('click')

    const options = wrapper.findAll('[role="option"]')
    expect(options).toHaveLength(2)
    expect(options[0].attributes('aria-selected')).toBe('false')
    expect(options[1].attributes('aria-selected')).toBe('true')

    await options[0].trigger('click')
    expect(save).toHaveBeenCalledTimes(1)
    expect(save.mock.calls[0][1]).toMatchObject({
      modelId: 'row-eu',
      provider: 'deepseek',
      model: 'deepseek-v4-flash',
    })
    // No effort in the request: a switch takes the row's configured one, which
    // only the server knows (plan 300).
    expect(save.mock.calls[0][1]).not.toHaveProperty('effort')
  })
})
