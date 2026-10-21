import { describe, expect, it } from 'vitest'

import { COLUMN_MAX_SIZE, COLUMN_MIN_SIZE, contentWidth } from './autofit'

/**
 * The arithmetic of a column autofit: the widest content the caller measured,
 * plus the padding a cell adds, held between the column's floor and ceiling.
 * The measurement itself needs a laid-out document and belongs to the e2e suite.
 */
describe('contentWidth', () => {
  it('answers the widest width plus the cell padding', () => {
    expect(contentWidth([120, 210], { padding: 20 })).toBe(230)
  })

  it('holds the answer inside the minimum, so an empty column keeps a real header', () => {
    expect(contentWidth([0], { padding: 20 })).toBe(COLUMN_MIN_SIZE)
    expect(contentWidth([], { padding: 20 })).toBe(COLUMN_MIN_SIZE)
  })

  it('holds the answer inside the maximum, so one long value cannot fill the viewport', () => {
    expect(contentWidth([5000], { padding: 20 })).toBe(COLUMN_MAX_SIZE)
    // A column declared with a smaller ceiling answers with that ceiling.
    expect(contentWidth([500], { padding: 20, max: 320 })).toBe(320)
  })

  it('lets a column declare its own floor', () => {
    expect(contentWidth([10], { padding: 0, min: 120 })).toBe(120)
  })

  it('rounds up to whole pixels', () => {
    expect(contentWidth([100.2], { padding: 19.6 })).toBe(120)
  })
})
