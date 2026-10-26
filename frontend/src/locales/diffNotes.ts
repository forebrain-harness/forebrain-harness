/**
 * The short notes under a shell card’s changed files (plan 130’s wording,
 * verbatim from the engine — pkg/tool/diff_blocks.go). A Chinese interface
 * reads them in Chinese: each engine sentence maps to its agreed wording,
 * and a note the table does not know stays exactly as it came rather than
 * being half-translated.
 */
import type { Component } from 'vue'
import { FilePlus2, FileX, Files, GitBranch, GitMerge } from 'lucide-vue-next'

/** The trailing marker a note carries when part of the diff was unavailable. */
const PART_UNAVAILABLE = ' (part of the diff is unavailable)'

/** One engine note and how it reads in Chinese. */
const NOTE_RULES: { re: RegExp; zh: (match: RegExpMatchArray) => string }[] = [
  { re: /^\(file diff skipped for this git command\)$/, zh: () => 'git 切换分支类命令，不比较文件' },
  {
    re: /^\(another command ran in this repository at the same time;/,
    zh: () => '同一仓库同时有别的命令在跑，改动可能记在任一条下',
  },
  {
    re: /^\(a git step in this command can move the working tree,/,
    zh: () => '命令里有 git 步骤，可能是它移动了工作区',
  },
  { re: /^… (\d+) more files? changed$/, zh: (m) => `还有 ${m[1]} 个文件改动` },
  {
    re: /^(\d+) files? changed \(binary, mode only or too large to show\)$/,
    zh: (m) => `${m[1]} 个文件改动（二进制、仅权限或太大，不显示）`,
  },
  {
    re: /^\(file diff unavailable for this command; (\d+) files? changed\)$/,
    zh: (m) => `这条命令的文件差异不可用（${m[1]} 个文件改动）`,
  },
  { re: /^\(file diff unavailable for this command\)$/, zh: () => '这条命令的文件差异不可用' },
]

/**
 * translateDiffNote words one note for the reader. English notes are the
 * engine’s own sentences and pass through; Chinese applies the table, with
 * the unavailable marker carried over in its Chinese wording.
 */
export function translateDiffNote(note: string, locale: 'zh' | 'en'): string {
  const base = note.endsWith(PART_UNAVAILABLE) ? note.slice(0, -PART_UNAVAILABLE.length) : note
  const suffix = note.endsWith(PART_UNAVAILABLE)
    ? (locale === 'zh' ? '（部分差异不可用）' : PART_UNAVAILABLE)
    : ''
  if (locale !== 'zh') return note
  for (const rule of NOTE_RULES) {
    const match = base.match(rule.re)
    if (match) return rule.zh(match) + suffix
  }
  return note
}

/**
 * The grey glyph ahead of a note, by what it says: more files, a file that
 * cannot be shown, a concurrent command, or a git step that moved the tree.
 */
export function diffNoteIcon(note: string): Component {
  if (note.startsWith('… ')) return Files
  if (note.startsWith('(another command ran')) return GitMerge
  if (note.startsWith('(file diff skipped') || note.startsWith('(a git step')) return GitBranch
  if (note.startsWith('(file diff unavailable') || /binary, mode only/.test(note)) return FileX
  return FilePlus2
}
