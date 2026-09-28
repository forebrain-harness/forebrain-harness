import { describe, expect, it } from 'vitest'
import {
  codeThemeDark,
  codeThemeInputs,
  codeThemeLight,
  codeThemeLightName,
  codeThemePair,
  markdownShikiOptions,
} from '@repo/elements/code-block'

// Code is painted with one theme across the whole product: the terminal
// renderer resolves chroma's monokai/monokailight, and every highlighter in the
// web client must resolve the same pair — a file cannot change colour because
// it is being shown by a different card or a different client.
describe('code theme', () => {
  it('names monokai and its light sibling', () => {
    expect(codeThemeDark).toBe('monokai')
    expect(codeThemeLightName).toBe('monokailight')
    expect(codeThemeLight.name).toBe(codeThemeLightName)
    expect(codeThemeLight.type).toBe('light')
    expect(codeThemePair).toEqual({ light: 'monokailight', dark: 'monokai' })
    expect(codeThemeInputs).toEqual([codeThemeLight, codeThemeDark])
  })

  it('hands the markdown renderer the same pair', () => {
    expect(markdownShikiOptions.theme).toEqual(['monokai', 'monokai'])
    expect(markdownShikiOptions.codeToTokenOptions).toEqual({
      themes: { light: codeThemeLight, dark: 'monokai' },
    })
  })

  it('carries a light canvas everywhere, with monokai emphasis intact', () => {
    expect(codeThemeLight.colors?.['editor.background']).toBe('#fafafa')
    expect(codeThemeLight.colors?.['editor.foreground']).toBe('#111111')

    const foregrounds = (codeThemeLight.tokenColors ?? [])
      .map(entry => entry.settings?.foreground)
      .filter((color): color is string => Boolean(color))
      .map(color => color.toLowerCase())

    expect(foregrounds.length).toBeGreaterThan(0)
    // Monokai's dark canvas colours must not survive the light counterpart.
    expect(foregrounds).not.toContain('#f8f8f2')
    expect(foregrounds).not.toContain('#272822')
    expect(foregrounds).not.toContain('#66d9ef')
    expect(foregrounds).not.toContain('#a6e22e')
    expect(foregrounds).not.toContain('#e6db74')
    // The counterpart's own palette, and the accents Monokai keeps in both.
    expect(foregrounds).toContain('#75af00')
    expect(foregrounds).toContain('#00a8c8')
    expect(foregrounds).toContain('#d88200')
    expect(foregrounds).toContain('#f92672')
    expect(foregrounds).toContain('#75715e')
  })
})
