import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { setLocale } from '@/locales'
import { effortLevelLabel } from './effortLabel'

/**
 * Every surface that writes a reasoning effort names it the same way: the
 * catalog's four levels, the explicit default, and a level the catalog adds
 * later shown as it is named — never blank, never a raw id the reader cannot
 * read.
 */
describe('effortLevelLabel', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  it('names the four catalog levels', () => {
    expect(['low', 'medium', 'high', 'xhigh'].map(effortLevelLabel)).toEqual(['Low', 'Medium', 'High', 'Highest'])
  })

  it('names the explicit default and passes an unknown level through', () => {
    expect(effortLevelLabel('')).toBe('Default')
    expect(effortLevelLabel('ultra')).toBe('ultra')
  })

  it('speaks the reader’s language', () => {
    setLocale('zh')
    expect(['low', 'xhigh', ''].map(effortLevelLabel)).toEqual(['低', '最高', '默认'])
    setLocale('en')
  })
})
