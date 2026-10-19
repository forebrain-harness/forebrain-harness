import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import type { SessionModelOption } from '@/lib/api'
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
  { provider: 'deepseek', model: 'deepseek-v4-pro', label: 'DeepSeek V4 Pro', efforts: ['low', 'medium', 'high', 'xhigh'], supportsImages: false },
  { provider: 'deepseek', model: 'deepseek-v4-flash', label: 'DeepSeek V4 Flash', efforts: [], supportsImages: true },
]

function mountChip(props: Record<string, unknown> = {}) {
  return mount(ModelChip, {
    props: {
      sessionId: 's1',
      selection: { provider: 'deepseek', model: 'deepseek-v4-pro', effort: '' },
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
})
