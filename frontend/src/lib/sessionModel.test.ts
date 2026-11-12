import { describe, expect, it } from 'vitest'

import { sessionModelOptionInForce } from './sessionModel'

/**
 * One lookup for the model in force: the effort chip on the conversation page
 * and the one in the skill workshop both read it, and both must answer with the
 * same row — the configured row the selection names, not the first row that
 * happens to share its provider and model.
 */
describe('sessionModelOptionInForce', () => {
  it('names the configured row the selection carries, not its twin', () => {
    const data = {
      modelId: 'row-us',
      provider: 'deepseek',
      model: 'deepseek-v4-flash',
      effort: '',
      options: [
        { id: 'row-eu', provider: 'deepseek', model: 'deepseek-v4-flash', label: 'eu', efforts: ['low'] },
        { id: 'row-us', provider: 'deepseek', model: 'deepseek-v4-flash', label: 'us', efforts: ['low', 'high'], defaultEffort: 'high' },
      ],
    }
    expect(sessionModelOptionInForce(data)).toBe(data.options[1])
  })

  it('falls back to the pair for a selection recorded before ids existed', () => {
    const data = {
      provider: 'deepseek',
      model: 'deepseek-v4-flash',
      effort: 'low',
      options: [
        { id: 'row-eu', provider: 'deepseek', model: 'deepseek-chat', label: 'chat', efforts: [] },
        { id: 'row-us', provider: 'deepseek', model: 'deepseek-v4-flash', label: 'flash', efforts: ['low', 'high'] },
      ],
    }
    expect(sessionModelOptionInForce(data)).toBe(data.options[1])
  })

  it('answers nothing before the page has read a model', () => {
    expect(sessionModelOptionInForce(null)).toBeUndefined()
  })
})
