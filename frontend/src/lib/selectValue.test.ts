import { describe, expect, it } from 'vitest'

import { EMPTY_CHOICE, fromChoice, toChoice } from './selectValue'

/**
 * The empty-string sentinel every migrated dropdown rides on: reka-ui's
 * SelectItem refuses `''`, so a real option whose value is empty travels as
 * EMPTY_CHOICE and must come back as `''` — while every other value, including
 * strings that merely look like the marker, passes through untouched.
 */
describe('selectValue', () => {
  it('round-trips the empty string through the sentinel', () => {
    expect(toChoice('')).toBe(EMPTY_CHOICE)
    expect(fromChoice(EMPTY_CHOICE)).toBe('')
    expect(fromChoice(toChoice(''))).toBe('')
  })

  it('treats null and undefined as the empty string', () => {
    expect(toChoice(null)).toBe(EMPTY_CHOICE)
    expect(toChoice(undefined)).toBe(EMPTY_CHOICE)
    expect(fromChoice(null)).toBe('')
    expect(fromChoice(undefined)).toBe('')
  })

  it('leaves every real value alone, in both directions', () => {
    for (const value of ['/chat/completions', '/responses', 'high', '@empty-ish', 'empty', '0']) {
      expect(toChoice(value)).toBe(value)
      expect(fromChoice(value)).toBe(value)
    }
  })

  it('keeps a bare sentinel from a text field out of the real values', () => {
    // The marker is not a plausible wire value, so nothing but the transform
    // itself can produce it; a value that merely starts with it survives.
    expect(fromChoice(`${EMPTY_CHOICE} `)).toBe(`${EMPTY_CHOICE} `)
    expect(toChoice(EMPTY_CHOICE)).toBe(EMPTY_CHOICE)
  })
})
