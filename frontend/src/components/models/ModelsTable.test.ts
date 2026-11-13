import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import tip from '@/directives/tip'
import type { LlmModelRow } from '@/lib/api'
import { setLocale } from '@/locales'
import ModelsTable from './ModelsTable.vue'

/**
 * The model cell reads the vendor listing's display name when the row carries
 * one, and the wire id otherwise — the same rule every model label follows.
 * The row's identity (data-model-row) stays the row id either way.
 */

beforeAll(() => {
  setLocale('en')
})

function row(overrides: Partial<LlmModelRow> = {}): LlmModelRow {
  return {
    id: 'r1',
    provider: 'qodercn',
    providerName: 'Qoder',
    custom: false,
    model: 'gfmodel',
    baseUrl: 'https://gateway.qoder.com.cn',
    apiPath: '/algo/api/v2/service/pro/sse/agent_chat_generation',
    urlVerbatim: false,
    subagentAllowed: false,
    protocol: 'chat/completions',
    endpoint: 'https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation',
    apiKeySet: false,
    loginBacked: true,
    caps: {},
    effortsKnown: false,
    catalogCaps: {},
    isDefault: false,
    createdAt: 0,
    ...overrides,
  }
}

function mountTable(rows: LlmModelRow[]) {
  return mount(ModelsTable, {
    props: { rows },
    global: { directives: { tip } },
  })
}

describe('ModelsTable', () => {
  it('shows the vendor listing display name in the model cell', () => {
    const wrapper = mountTable([row({ modelName: 'GLM-5.3-Flash' })])
    const cell = wrapper.get('[data-testid="models-cell-model"]')
    expect(cell.text()).toBe('GLM-5.3-Flash')
    expect(wrapper.find('[data-model-row="r1"]').exists()).toBe(true)
  })

  it('keeps the wire id when the listing named none', () => {
    const wrapper = mountTable([row()])
    expect(wrapper.get('[data-testid="models-cell-model"]').text()).toBe('gfmodel')
  })
})