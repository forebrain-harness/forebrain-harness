import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import UsableModelsCard from './UsableModelsCard.vue'
import type { ReadableAnswer, ReadableModel } from '@/composables/useChatStream'

/**
 * The roster a list_usable_models answer draws (plan 337): provider headings in
 * the answer's own order, one line per model, thinking levels as chips with the
 * row's default highlighted, and the current/handed-off marks. The wire id is
 * never drawn — it is not even on the DTO.
 */
describe('UsableModelsCard', () => {
  beforeAll(() => setLocale('zh'))

  const models = (rows: Partial<ReadableModel>[]): ReadableAnswer => ({
    rows: [],
    models: rows.map((row) => ({ provider: '', model: '', ...row }) as ReadableModel),
  })

  it('draws one group per provider, in the answer order, and chips the levels', () => {
    const wrapper = mount(UsableModelsCard, {
      props: {
        answer: models([
          { provider: 'zhipuai', model: 'glm-5.3', efforts: ['low', 'medium', 'high'], defaultEffort: 'medium', current: true },
          { provider: 'zhipuai', model: 'glm-5.3-air', efforts: ['low', 'medium'], defaultEffort: 'low' },
          { provider: 'qodercn', model: 'DeepSeek-Flash', handedOff: true },
          { provider: 'qodercn', model: 'DeepSeek-R1', efforts: ['low', 'medium', 'high'], defaultEffort: 'high' },
        ]),
      },
    })

    const groups = wrapper.findAll('[data-testid="usable-models-group"]')
    expect(groups).toHaveLength(2)
    expect(groups[0].find('.prov').text()).toBe('zhipuai')
    expect(groups[1].find('.prov').text()).toBe('qodercn')

    const rows = wrapper.findAll('[data-testid="usable-models-row"]')
    expect(rows).toHaveLength(4)
    expect(rows[0].find('.nm').text()).toBe('glm-5.3')
    expect(rows[0].find('.flag.cur').text()).toBe('当前')
    expect(rows[1].find('.l2 .chip').exists()).toBe(true)

    // Every level is a chip; the row's default carries the highlight and the tag.
    const chips = rows[0].findAll('.l2 .chip')
    expect(chips.map((chip) => chip.text())).toEqual(['low', 'medium默认', 'high'])
    expect(chips[1].classes()).toContain('def')
    expect(chips[0].classes()).not.toContain('def')

    // A row with no levels draws no chip line; the hand-off mark is an outline pill.
    expect(rows[2].find('.l2').exists()).toBe(false)
    expect(rows[2].find('.flag.ho').text()).toBe('交接')
  })

  it('says the roster is empty instead of drawing nothing, and words it per language', () => {
    const zh = mount(UsableModelsCard, { props: { answer: models([]) } })
    expect(zh.find('[data-testid="usable-models-empty"]').text()).toContain('没有可派发给子代理的模型。')
    expect(zh.findAll('[data-testid="usable-models-row"]')).toHaveLength(0)

    setLocale('en')
    const en = mount(UsableModelsCard, { props: { answer: { rows: [], note: 'No model is allowed.' } } })
    expect(en.find('[data-testid="usable-models-empty"]').text()).toContain('No model can be dispatched to a subagent.')
  })

  it('never draws the wire id, which is no field of the DTO at all', () => {
    const answer = models([{ provider: 'zhipuai', model: 'glm-5.3', efforts: ['low'], defaultEffort: 'low' }])
    // A caller that still carries the raw row (with its id) must see only the
    // card's own facts.
    ;(answer.models![0] as unknown as Record<string, unknown>).id = 'llm_secret_id'
    const wrapper = mount(UsableModelsCard, { props: { answer } })
    expect(wrapper.text()).not.toContain('llm_secret_id')
  })
})
