import { afterAll, describe, expect, it } from 'vitest'

import { setLocale, t } from '@/locales'
import { modelPinLabel, modelPinLabels } from './modelPins'

/**
 * One pin, one page, one language (plan 347): the API sends a machine kind and
 * this page writes the words, because the page's language lives in localStorage
 * and never reaches the request's Accept-Language.
 */
describe('modelPinLabel', () => {
  afterAll(() => setLocale('en'))

  it('names every kind in Chinese', () => {
    setLocale('zh')
    expect(modelPinLabel(t, { kind: 'plan_review' })).toBe('计划评审')
    expect(modelPinLabel(t, { kind: 'goal_evaluator' })).toBe('目标评估')
    expect(modelPinLabel(t, { kind: 'subagent', name: 'explore' })).toBe('子代理 explore')
  })

  it('names every kind in English', () => {
    setLocale('en')
    expect(modelPinLabel(t, { kind: 'plan_review' })).toBe('plan review')
    expect(modelPinLabel(t, { kind: 'goal_evaluator' })).toBe('goal evaluator')
    expect(modelPinLabel(t, { kind: 'subagent', name: 'explore' })).toBe('subagent explore')
  })

  it('lists the pins in the order the API sent them, in both languages', () => {
    setLocale('zh')
    const pins = [{ kind: 'subagent', name: 'explore' }, { kind: 'plan_review' }]
    expect(modelPinLabels(t, pins)).toEqual(['子代理 explore', '计划评审'])
    setLocale('en')
    expect(modelPinLabels(t, pins)).toEqual(['subagent explore', 'plan review'])
    expect(modelPinLabels(t, undefined)).toEqual([])
  })

  it('leaves no trailing space on a subagent the config never named', () => {
    // The config emits no nameless subagent pin today; the label still reads as
    // a word rather than 「子代理 」 if one ever arrives.
    setLocale('zh')
    expect(modelPinLabel(t, { kind: 'subagent' })).toBe('子代理')
    setLocale('en')
    expect(modelPinLabel(t, { kind: 'subagent' })).toBe('subagent')
  })
})
