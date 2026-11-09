import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { setLocale } from '@/locales'
import { EFFORT_CHOICES, effortLevelLabel } from './effortLabel'

/**
 * Every surface that writes a reasoning effort names it the same way: the seven
 * levels the project settled on, the explicit default, and a level a source adds
 * later shown as it is named — never blank, never a raw id the reader cannot
 * read.
 */
describe('effortLevelLabel', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  it('names the seven levels', () => {
    expect(['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'].map(effortLevelLabel)).toEqual([
      'Off',
      'Minimal',
      'Low',
      'Medium',
      'High',
      'xHigh',
      'Max',
    ])
  })

  it('names the explicit default and passes an unknown level through', () => {
    expect(effortLevelLabel('')).toBe('Default')
    expect(effortLevelLabel('ultra')).toBe('ultra')
  })

  it('speaks the reader’s language', () => {
    setLocale('zh')
    expect(['none', 'minimal', 'low', 'xhigh', 'max', ''].map(effortLevelLabel)).toEqual([
      '关闭思考',
      '极低',
      '低',
      '最高',
      '极致',
      '默认',
    ])
    setLocale('en')
  })

  it('offers the six pickable levels, `none` being a capability', () => {
    expect(EFFORT_CHOICES).toEqual(['minimal', 'low', 'medium', 'high', 'xhigh', 'max'])
  })
})
