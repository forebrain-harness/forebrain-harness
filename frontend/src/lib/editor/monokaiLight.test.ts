import { describe, expect, it } from 'vitest'

import { codeThemeLight, monokaiLightPalette } from '@repo/elements/code-block'

import { monokaiLightHighlightStyle } from './monokaiLight'

// The editor and the assistant's replies paint code with the same palette, so
// the CodeMirror highlight style must resolve every colour it uses from
// `monokaiLightPalette` — no colour may be written down twice.
const paletteValues = new Set<string>(Object.values(monokaiLightPalette))
const syntaxValues = new Set<string>(
  Object.entries(monokaiLightPalette)
    .filter(([key]) => key !== 'background' && key !== 'foreground')
    .map(([, value]) => value),
)

function foregroundsUsedByStyle(): string[] {
  return monokaiLightHighlightStyle.specs
    .map((spec) => (spec as { color?: string }).color)
    .filter((color): color is string => Boolean(color))
}

describe('editor monokai-light highlight', () => {
  it('takes every colour from the shared palette', () => {
    const used = foregroundsUsedByStyle()
    expect(used.length).toBeGreaterThan(0)
    for (const colour of used) {
      expect(paletteValues.has(colour)).toBe(true)
    }
  })

  it('covers exactly the palette’s syntax colours', () => {
    // Background and foreground are the editor canvas, painted by CSS rather
    // than by a token style, so they are the only palette entries not used.
    expect(new Set(foregroundsUsedByStyle())).toEqual(syntaxValues)
  })

  it('leaves the Shiki theme on the same palette', () => {
    // The refactor that routed the Shiki theme through the palette must not
    // have moved a colour. The three editor-annotation tokens and the alpha
    // literal have no palette entry of their own.
    const allowed = new Set(
      [...paletteValues, '#2f6fd0', '#a06a00', '#7b3fbf', '#ae81ffa0'].map((colour) => colour.toLowerCase()),
    )
    const foregrounds = (codeThemeLight.tokenColors ?? [])
      .map((entry) => entry.settings?.foreground)
      .filter((colour): colour is string => Boolean(colour))
      .map((colour) => colour.toLowerCase())
    expect(foregrounds.length).toBeGreaterThan(0)
    for (const colour of foregrounds) {
      expect(allowed.has(colour)).toBe(true)
    }
  })
})
