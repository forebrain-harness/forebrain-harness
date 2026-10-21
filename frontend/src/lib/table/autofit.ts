/**
 * Column autofit: how wide a column has to be for its content to fit on one
 * line.
 *
 * The measurement itself is the caller's — it needs a laid-out document, and
 * what it must not do is worth writing down here: a truncated block reports the
 * width of its *box* in `scrollWidth`, not the width its text wants, so the
 * numbers handed in have to come from the content's own geometry (see
 * `wantedWidth` in `DataTable.vue`). What this file owns is the arithmetic
 * around them: the padding a cell adds, the floor and ceiling a column has, and
 * the empty column.
 */

/** The narrowest and widest a column may be dragged or auto-fitted to. */
export const COLUMN_MIN_SIZE = 56
export const COLUMN_MAX_SIZE = 900

export interface AutofitOptions {
  /** The horizontal padding one cell carries, both sides together, in px. */
  padding?: number
  /** The narrowest the answer may be; the table's own default by default. */
  min?: number
  /** The widest the answer may be; the table's own default by default. */
  max?: number
}

/**
 * The width the widest of `widths` needs to show its content on one line, plus
 * the cell padding, held inside `[min, max]`.
 *
 * A cell of nothing is skipped rather than counted as width 0: a column whose
 * rows hold nothing has no content width to answer with, and the minimum is the
 * honest answer.
 */
export function contentWidth(widths: Iterable<number>, options: AutofitOptions = {}): number {
  const padding = options.padding ?? 0
  const min = options.min ?? COLUMN_MIN_SIZE
  const max = options.max ?? COLUMN_MAX_SIZE

  let widest = 0
  for (const width of widths) {
    if (width > widest) widest = width
  }
  return Math.min(Math.max(Math.ceil(widest + padding), min), max)
}
