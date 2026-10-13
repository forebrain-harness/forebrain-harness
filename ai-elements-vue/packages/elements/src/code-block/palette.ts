/**
 * Monokai Light — the single palette every forebrain surface paints code with.
 *
 * The colours are chroma's `monokailight` (the TUI's light code style, see
 * `pkg/tui/render.go` `codeStyleLight`), and they are the whole story: the
 * Shiki highlighter that renders code inside an assistant reply and the
 * CodeMirror highlighter that renders the file editor both read from these
 * constants, so a token does not change colour because a different client, or
 * a different editor, happens to be showing it. Do not write a code colour
 * anywhere else — add it here and take it from here.
 *
 * The names are the chroma token families, not the lezer tags the editor maps
 * them onto: the tag → token mapping lives in the editor's own highlight module
 * (`frontend/src/lib/editor/monokaiLight.ts`).
 */
export const monokaiLightPalette = {
  keyword: '#00a8c8',
  namespace: '#f92672',
  name: '#111111',
  green: '#75af00',
  tag: '#f92672',
  literal: '#ae81ff',
  string: '#d88200',
  escape: '#8045ff',
  operator: '#f92672',
  punctuation: '#111111',
  comment: '#75715e',
  error: '#960050',
  background: '#fafafa',
  foreground: '#272822',
} as const
