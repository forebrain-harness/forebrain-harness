import type { BundledLanguage, SpecialLanguage } from 'shiki'

/**
 * File path → Shiki language id.
 *
 * The terminal renderer asks chroma to match a lexer by filename; the browser
 * has no such matcher, so the mapping is spelled out here. An unknown
 * extension resolves to plain text, which Shiki tokenises as a single run —
 * the code still reads, it simply carries no emphasis.
 */
const BY_EXTENSION: Record<string, BundledLanguage> = {
  bash: 'bash',
  c: 'c',
  cc: 'cpp',
  cpp: 'cpp',
  cs: 'csharp',
  css: 'css',
  dart: 'dart',
  diff: 'diff',
  go: 'go',
  graphql: 'graphql',
  h: 'c',
  hpp: 'cpp',
  htm: 'html',
  html: 'html',
  ini: 'ini',
  java: 'java',
  js: 'javascript',
  json: 'json',
  jsonc: 'jsonc',
  jsx: 'jsx',
  kt: 'kotlin',
  kts: 'kotlin',
  less: 'less',
  lua: 'lua',
  md: 'markdown',
  mdx: 'mdx',
  mjs: 'javascript',
  patch: 'diff',
  php: 'php',
  proto: 'proto',
  py: 'python',
  r: 'r',
  rb: 'ruby',
  rs: 'rust',
  scala: 'scala',
  scss: 'scss',
  sh: 'shellscript',
  sql: 'sql',
  svelte: 'svelte',
  swift: 'swift',
  tf: 'terraform',
  toml: 'toml',
  ts: 'typescript',
  tsx: 'tsx',
  vue: 'vue',
  xml: 'xml',
  yaml: 'yaml',
  yml: 'yaml',
  zsh: 'bash',
}

/** Files the extension rule cannot reach, keyed by lowercased base name. */
const BY_NAME: Record<string, BundledLanguage> = {
  'dockerfile': 'docker',
  'makefile': 'make',
  '.gitignore': 'ini',
  '.env': 'ini',
}

export const PLAIN_LANGUAGE: SpecialLanguage = 'text'

export function languageForPath(path?: string): BundledLanguage | SpecialLanguage {
  const base = (path ?? '').split(/[\\/]/).pop()?.toLowerCase() ?? ''
  if (!base) {
    return PLAIN_LANGUAGE
  }
  if (BY_NAME[base]) {
    return BY_NAME[base]
  }
  const dot = base.lastIndexOf('.')
  if (dot <= 0) {
    return PLAIN_LANGUAGE
  }
  return BY_EXTENSION[base.slice(dot + 1)] ?? PLAIN_LANGUAGE
}
