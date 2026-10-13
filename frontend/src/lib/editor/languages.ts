import { LanguageDescription, LanguageSupport, StreamLanguage } from '@codemirror/language'

/**
 * The code languages the editor offers: a curated list rather than
 * `@codemirror/language-data`'s full catalogue. Bash, Go, JSON, YAML,
 * TypeScript, JavaScript, Python, SQL, Markdown, Diff cover the files a
 * forebrain workspace actually holds, and each grammar loads on demand, so a
 * Markdown file never carries the SQL parser.
 *
 * Crepe's code blocks default to `language-data`'s whole list; the code-block
 * feature is handed this array instead (see FileEditor.vue), which both trims
 * the bundle and keeps the language picker to what the server can render.
 */
export const languages: readonly LanguageDescription[] = [
  LanguageDescription.of({
    name: 'Bash',
    alias: ['bash', 'sh', 'shell', 'zsh'],
    extensions: ['sh', 'bash', 'zsh'],
    load: () =>
      import('@codemirror/legacy-modes/mode/shell').then((m) => new LanguageSupport(StreamLanguage.define(m.shell))),
  }),
  LanguageDescription.of({
    name: 'Go',
    alias: ['go', 'golang'],
    extensions: ['go'],
    load: () => import('@codemirror/lang-go').then((m) => m.go()),
  }),
  LanguageDescription.of({
    name: 'JSON',
    alias: ['json'],
    extensions: ['json'],
    load: () => import('@codemirror/lang-json').then((m) => m.json()),
  }),
  LanguageDescription.of({
    name: 'YAML',
    alias: ['yaml', 'yml'],
    extensions: ['yaml', 'yml'],
    load: () => import('@codemirror/lang-yaml').then((m) => m.yaml()),
  }),
  LanguageDescription.of({
    name: 'TypeScript',
    alias: ['ts', 'typescript'],
    extensions: ['ts', 'tsx'],
    load: () => import('@codemirror/lang-javascript').then((m) => m.javascript({ typescript: true })),
  }),
  LanguageDescription.of({
    name: 'JavaScript',
    alias: ['js', 'javascript'],
    extensions: ['js', 'mjs'],
    load: () => import('@codemirror/lang-javascript').then((m) => m.javascript()),
  }),
  LanguageDescription.of({
    name: 'Python',
    alias: ['py', 'python'],
    extensions: ['py'],
    load: () => import('@codemirror/lang-python').then((m) => m.python()),
  }),
  LanguageDescription.of({
    name: 'SQL',
    alias: ['sql'],
    extensions: ['sql'],
    load: () => import('@codemirror/lang-sql').then((m) => m.sql()),
  }),
  LanguageDescription.of({
    name: 'Markdown',
    alias: ['md', 'markdown'],
    extensions: ['md'],
    load: () => import('@codemirror/lang-markdown').then((m) => m.markdown()),
  }),
  LanguageDescription.of({
    name: 'Diff',
    alias: ['diff', 'patch'],
    extensions: ['diff', 'patch'],
    load: () =>
      import('@codemirror/legacy-modes/mode/diff').then((m) => new LanguageSupport(StreamLanguage.define(m.diff))),
  }),
]

/** The lowercase extension of a path, without the dot ('' when there is none). */
export function extensionOf(path: string): string {
  const match = /\.([^.\\/]+)$/.exec(path)
  return match ? match[1].toLowerCase() : ''
}

/**
 * The language a path belongs to, by extension — the same lookup CodeMirror
 * does internally, exposed so the editor's footer can name it and so a file
 * can pick its grammar without a language picker.
 */
export function languageForPath(path: string): LanguageDescription | undefined {
  return LanguageDescription.matchFilename(languages, path) ?? undefined
}
