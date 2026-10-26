/**
 * The shell command, laid out for the document tab (owner 2026-10-25): each
 * source line, each step of a long chain on its own row, a heredoc's body as
 * an indented block under its opening line. Ported from the prototype's
 * shSplit/shHi/shSteps (app-preview.html) as structured data — the Vue
 * template renders it, so no HTML is ever assembled here.
 */

/** One token of one step, in the greys the prototype sets it in. */
export interface ShellToken {
  kind: 'cmd' | 'flag' | 'str' | 'var' | 'op' | 'text'
  text: string
}

/** One step row: a chain link, or a whole short line. */
export interface ShellStepRow {
  kind: 'step'
  /** The connector that leads into this step ('&&', '||', '|', ';', or ''). */
  op: string
  tokens: ShellToken[]
}

/** A heredoc's body, read as one block until its closing marker line. */
export interface ShellHeredocRow {
  kind: 'heredoc'
  op: string
  body: string
}

/** The heredoc's closing marker line. */
export interface ShellEndRow {
  kind: 'end'
  op: string
  text: string
}

export type ShellRow = ShellStepRow | ShellHeredocRow | ShellEndRow

/** A line longer than this is split at its top-level connectors. */
const SPLIT_AT = 56

/**
 * shSplit breaks one line of shell at its top-level `&&` / `||` / `|` / `;`
 * (never inside quotes), so a long chain reads one step per line.
 */
function shSplit(line: string): { op: string; text: string }[] {
  const out: { op: string; text: string }[] = []
  let cur = ''
  let op = ''
  let quote = ''
  const push = () => {
    const text = cur.trim()
    if (text) out.push({ op, text })
    cur = ''
  }
  for (let i = 0; i < line.length; i++) {
    const c = line[i]!
    const two = line.substr(i, 2)
    if (quote) {
      cur += c
      if (c === '\\' && quote === '"' && i + 1 < line.length) cur += line[++i]
      else if (c === quote) quote = ''
      continue
    }
    if (c === "'" || c === '"') {
      quote = c
      cur += c
      continue
    }
    if (two === '&&' || two === '||') {
      push()
      op = two
      i++
      continue
    }
    if (c === '|' || c === ';') {
      push()
      op = c
      continue
    }
    cur += c
  }
  push()
  return out
}

/**
 * shHi sets one step in type: the program carries the weight, everything else
 * stays grey; strings take the one warm tone, variables the one cool one.
 * `sudo` and a `VAR=x` prefix are not the program's name.
 */
function shHi(text: string): ShellToken[] {
  const re = /('[^']*'?|"(?:[^"\\]|\\.)*"?)|(\s+)|(2>&1|&>|>>|<<-?|[<>])|(\$\{[^}]*\}|\$[A-Za-z_]\w*)|(--?[A-Za-z][\w.-]*)|([^\s'"<>]+)/g
  const tokens: ShellToken[] = []
  let named = false
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m[1]) tokens.push({ kind: 'str', text: m[1] })
    else if (m[2]) tokens.push({ kind: 'text', text: m[2] })
    else if (m[3]) tokens.push({ kind: 'op', text: m[3] })
    else if (m[4]) tokens.push({ kind: 'var', text: m[4] })
    else if (m[5] && named) tokens.push({ kind: 'flag', text: m[5] })
    else {
      const word = m[5] ?? m[6] ?? ''
      if (!named && word !== 'sudo' && !/^[A-Za-z_]\w*=/.test(word)) {
        named = true
        tokens.push({ kind: 'cmd', text: word })
      } else {
        tokens.push({ kind: 'text', text: word })
      }
    }
  }
  return tokens
}

/**
 * shellSteps lays the whole command out: each source line, each step of a
 * long chain on its own row, a heredoc's body as a block under its opening
 * line. `multi` says more than one row — the column the connectors sit in is
 * drawn only then.
 */
export function shellSteps(cmd: string): { rows: ShellRow[]; multi: boolean } {
  const lines = String(cmd ?? '').split('\n')
  const rows: ShellRow[] = []
  for (let i = 0; i < lines.length; i++) {
    const ln = lines[i]!
    const hd = /<<-?\s*(['"]?)([A-Za-z_]\w*)\1/.exec(ln)
    const steps = ln.length > SPLIT_AT ? shSplit(ln) : [{ op: '', text: ln.trim() }]
    for (const step of steps) {
      if (step.text) rows.push({ kind: 'step', op: step.op, tokens: shHi(step.text) })
    }
    if (!hd) continue
    const end = hd[2]!
    const body: string[] = []
    for (i = i + 1; i < lines.length && lines[i]!.trim() !== end; i++) body.push(lines[i]!)
    rows.push({ kind: 'heredoc', op: '', body: body.join('\n') })
    if (i < lines.length) rows.push({ kind: 'end', op: '', text: lines[i]!.trim() })
  }
  return { rows, multi: rows.length > 1 }
}
