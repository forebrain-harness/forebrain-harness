/**
 * The one diff parser every card shares (owner 2026-10-25): a diff reads the
 * same in a shell card's changed files, an edited card, an approval's preview
 * and the workbench's compare — rows with a faint single line-number column,
 * a colour bar marking added and removed lines, and one fold mark between two
 * hunks. The row component that draws them is DiffBlock.vue.
 */

/** One row of a diff as a card draws it. */
export type DiffRow = {
  /** ' ' context, '+' added, '-' removed, '…' the fold between two hunks. */
  mark: ' ' | '+' | '-' | '…'
  /** The row's line number: the new file's for context and added lines, the old file's for removed ones. */
  line: number | null
  text: string
}

/** The `@@ -a,b +c,d @@` header of one hunk, with its two counters. */
const HUNK_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

/**
 * parseUnifiedDiff turns a unified diff (git's, or the engine's turn diff)
 * into the rows a card draws: context and added lines carry the new file's
 * line number, removed lines the old file's, and the break between two
 * hunks is one '…' row. Header lines (diff --git, index, ---, +++) are
 * dropped; so is "\ No newline at end of file", which is a fact about the
 * previous line, not a row of its own.
 */
export function parseUnifiedDiff(text: string): DiffRow[] {
  const rows: DiffRow[] = []
  let oldNo = 0
  let newNo = 0
  let inHunks = false
  // A unified diff ends with a newline; keeping the empty string it leaves
  // behind would draw one phantom context row after the last line.
  const body = String(text ?? '').endsWith('\n') ? String(text ?? '').slice(0, -1) : String(text ?? '')
  for (const raw of body.split('\n')) {
    const header = HUNK_RE.exec(raw)
    if (header) {
      if (inHunks) rows.push({ mark: '…', line: null, text: '' })
      oldNo = Number(header[1])
      newNo = Number(header[3])
      inHunks = true
      continue
    }
    if (!inHunks) continue // a file header: diff --git, index, ---, +++
    if (raw.startsWith('\\')) continue // "\ No newline at end of file"
    if (raw.startsWith('+')) {
      rows.push({ mark: '+', line: newNo, text: raw.slice(1) })
      newNo++
      continue
    }
    if (raw.startsWith('-')) {
      rows.push({ mark: '-', line: oldNo, text: raw.slice(1) })
      oldNo++
      continue
    }
    if (raw.startsWith(' ')) {
      rows.push({ mark: ' ', line: newNo, text: raw.slice(1) })
      oldNo++
      newNo++
      continue
    }
    // An empty line in a diff whose lines carry no leading space (difflib
    // keeps one; some producers trim it) is a context line of empty text.
    if (raw === '') {
      rows.push({ mark: ' ', line: newNo, text: '' })
      oldNo++
      newNo++
    }
  }
  return rows
}

/**
 * diffRowsFromLines maps the engine's own structured diff lines — the turn
 * diff a message ends with, whose hunks already carry per-line kinds and
 * numbers — onto the same rows, without rebuilding a unified diff only to
 * parse it back.
 */
export function diffRowsFromLines(
  hunks: { lines: { kind: 'add' | 'del' | 'ctx'; old_no?: number; new_no?: number; text: string }[] }[],
): DiffRow[] {
  const rows: DiffRow[] = []
  hunks.forEach((hunk, index) => {
    if (index > 0) rows.push({ mark: '…', line: null, text: '' })
    for (const line of hunk.lines) {
      if (line.kind === 'add') rows.push({ mark: '+', line: line.new_no ?? null, text: line.text })
      else if (line.kind === 'del') rows.push({ mark: '-', line: line.old_no ?? null, text: line.text })
      else rows.push({ mark: ' ', line: line.new_no ?? null, text: line.text })
    }
  })
  return rows
}
