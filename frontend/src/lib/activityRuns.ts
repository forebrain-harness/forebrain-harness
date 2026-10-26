/**
 * The activity line's grouping (owner 2026-10-25): the action cards of a turn
 * that follow one another — the agent's words, its thinking, its calls, a
 * subagent dispatch, the todo card, a settled approval line — share one
 * vertical line. Anything else (a user message, a compaction card, a goal
 * line, an error block) breaks the line: a run starts where the reader's own
 * words or the lifecycle's own cards say it does.
 */

/** One run of consecutive on-line blocks, or one block that stands alone. */
export type ActivityRun<T> = { line: true; items: T[] } | { line: false; item: T }

/**
 * groupActivityRuns folds a block list into runs. `onLine` names the kinds
 * that hang on the line; everything else passes through untouched, in place.
 */
export function groupActivityRuns<T extends { kind: string }>(
  blocks: T[],
  onLine: (block: T) => boolean,
): ActivityRun<T>[] {
  const out: ActivityRun<T>[] = []
  let run: T[] = []
  const flush = () => {
    if (run.length) out.push({ line: true, items: run })
    run = []
  }
  for (const block of blocks) {
    if (onLine(block)) {
      run.push(block)
      continue
    }
    flush()
    out.push({ line: false, item: block })
  }
  flush()
  return out
}

/** The kinds that hang on the activity line. */
const ON_LINE_KINDS = new Set(['assistant', 'thinking', 'tool', 'subagent', 'plan', 'approval'])

/**
 * blockOnActivityLine reports whether a timeline block hangs on the activity
 * line. A settled approval's one-line confirmation does (with no node of its
 * own); a live gate does not — it is the question the reader must answer, not
 * a step of the run.
 */
export function blockOnActivityLine(block: { kind: string }): boolean {
  return ON_LINE_KINDS.has(block.kind)
}
