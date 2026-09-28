import type { BundledLanguage, BundledTheme, CodeToTokensOptions, ThemeRegistration } from 'shiki'
import { monokaiLight } from './monokai-light'

/**
 * Monokai is the one syntax theme every forebrain surface paints code with — the
 * terminal renderer and the web client, a read_file result and the diff inside
 * an edit_file card alike. A file does not change colour depending on which
 * card, or which client, happens to be showing it, so the theme is named once
 * here and every highlighter in the web app resolves it from these constants.
 */
export const codeThemeDark = 'monokai'

/** The light-mode sibling; see monokai-light.ts for how it is derived. */
export const codeThemeLight: ThemeRegistration = monokaiLight

/** The light theme's registered name, usable once it has been loaded. */
export const codeThemeLightName = 'monokailight'

/**
 * The themes a highlighter must carry, in the order Shiki's `createHighlighter`
 * expects them. The light theme is passed as a registration because Shiki
 * bundles no Monokai Light to name.
 */
export const codeThemeInputs = [codeThemeLight, codeThemeDark]

/** The light/dark pair `codeToTokens` renders with, by name. */
export const codeThemePair = {
  light: codeThemeLightName,
  dark: codeThemeDark,
} as const

/**
 * Shiki settings for the Markdown renderer, which owns its own highlighter.
 * It preloads only themes it can find in Shiki's bundle, so Monokai is named
 * for both slots there; the pair actually rendered is handed to the tokeniser
 * below, and Shiki registers the light sibling from its registration on first
 * use.
 */
export const markdownShikiOptions: {
  theme: [BundledTheme, BundledTheme]
  codeToTokenOptions: CodeToTokensOptions<BundledLanguage, BundledTheme>
} = {
  theme: [codeThemeDark, codeThemeDark],
  codeToTokenOptions: {
    themes: {
      light: codeThemeLight,
      dark: codeThemeDark,
    },
  },
}
