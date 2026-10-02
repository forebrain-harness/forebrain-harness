import { beforeEach, describe, expect, it, vi } from 'vitest'

import { applyStoredAppearance, BRAND_SCHEMES, useAppearance } from './useAppearance'

function storage(): Storage {
  return window.localStorage
}

describe('useAppearance', () => {
  beforeEach(() => {
    storage().clear()
    delete document.documentElement.dataset.brand
    vi.restoreAllMocks()
  })

  it('defaults to navy', () => {
    applyStoredAppearance()
    expect(document.documentElement.dataset.brand).toBe('navy')
  })

  it('sets the brand on the document root and persists it', () => {
    applyStoredAppearance()
    const { setBrand } = useAppearance()
    setBrand('teal')
    expect(document.documentElement.dataset.brand).toBe('teal')
    expect(storage().getItem('forebrain-brand')).toBe('teal')
  })

  it('falls back to navy for an invalid stored value', () => {
    storage().setItem('forebrain-brand', 'pink')
    applyStoredAppearance()
    expect(document.documentElement.dataset.brand).toBe('navy')
  })

  it('accepts every declared scheme', () => {
    const { setBrand } = useAppearance()
    for (const scheme of BRAND_SCHEMES) {
      setBrand(scheme)
      expect(document.documentElement.dataset.brand).toBe(scheme)
    }
  })

  it('toggles the rail theme and persists the choice', () => {
    applyStoredAppearance()
    const { railTheme, toggleRailTheme } = useAppearance()
    const first = railTheme.value
    toggleRailTheme()
    expect(railTheme.value).toBe(first === 'dark' ? 'light' : 'dark')
    expect(storage().getItem('forebrain-theme')).toBe(railTheme.value)
  })
})
