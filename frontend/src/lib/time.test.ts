import { describe, expect, it } from 'vitest'

import { formatClock, formatDateTime, formatMonthDayTime } from './time'

describe('formatDateTime', () => {
  it('draws the local date and time to the minute', () => {
    // Built from a local date, so the expectation holds in every time zone.
    const stamp = Math.floor(new Date(2026, 0, 5, 9, 7, 42).getTime() / 1000)
    expect(formatDateTime(stamp)).toBe('2026-01-05 09:07')
  })

  it('pads every part to two digits', () => {
    const stamp = Math.floor(new Date(2026, 10, 3, 4, 5).getTime() / 1000)
    expect(formatDateTime(stamp)).toBe('2026-11-03 04:05')
  })

  it('draws an em dash for a value the row does not have', () => {
    expect(formatDateTime(undefined)).toBe('—')
    expect(formatDateTime(0)).toBe('—')
  })
})

describe('formatMonthDayTime', () => {
  it('draws the local month and day and the time to the minute', () => {
    // Built from a local date, so the expectation holds in every time zone.
    const stamp = Math.floor(new Date(2026, 0, 5, 9, 7, 42).getTime() / 1000)
    expect(formatMonthDayTime(stamp)).toBe('01-05 09:07')
  })

  it('pads every part to two digits and leaves the year out', () => {
    const stamp = Math.floor(new Date(2026, 10, 3, 4, 5).getTime() / 1000)
    expect(formatMonthDayTime(stamp)).toBe('11-03 04:05')
  })

  it('draws an em dash for a value the row does not have', () => {
    expect(formatMonthDayTime(undefined)).toBe('—')
    expect(formatMonthDayTime(0)).toBe('—')
  })
})

describe('formatClock', () => {
  it('draws the local time alone, to the minute', () => {
    // Built from a local date, so the expectation holds in every time zone.
    const stamp = Math.floor(new Date(2026, 0, 5, 9, 7, 42).getTime() / 1000)
    expect(formatClock(stamp)).toBe('09:07')
  })

  it('pads both parts to two digits', () => {
    const stamp = Math.floor(new Date(2026, 10, 3, 4, 5).getTime() / 1000)
    expect(formatClock(stamp)).toBe('04:05')
  })

  it('draws an em dash for a value the row does not have', () => {
    expect(formatClock(undefined)).toBe('—')
    expect(formatClock(0)).toBe('—')
  })
})
