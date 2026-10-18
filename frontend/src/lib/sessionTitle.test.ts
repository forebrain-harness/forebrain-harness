import { describe, expect, it } from 'vitest'

import { sessionTitleFromContent } from './sessionTitle'

/**
 * The engine's own title rule (pkg/state generateSessionTitle), which the web
 * surface applies before it has the server's title: first line, trimmed, at
 * most 50 characters then "...". An empty message has no temporary title at
 * all — the top bar falls back to the page's own word instead of inventing one.
 */
describe('sessionTitleFromContent', () => {
  it('keeps the first line of a multi-line message', () => {
    expect(sessionTitleFromContent('fix the login bug\nand then the header')).toBe('fix the login bug')
    expect(sessionTitleFromContent('carriage return first\r\nsecond')).toBe('carriage return first')
  })

  it('trims the message and the line it keeps', () => {
    expect(sessionTitleFromContent('   padded title   ')).toBe('padded title')
    expect(sessionTitleFromContent('\n\n  after blank lines\nmore')).toBe('after blank lines')
  })

  it('caps a long line at 50 characters and marks the cut', () => {
    const fifty = '中'.repeat(50)
    expect(sessionTitleFromContent(fifty)).toBe(fifty)
    expect(sessionTitleFromContent(`${fifty}多`)).toBe(`${fifty}...`)
    const fortyNine = '字'.repeat(49)
    expect(sessionTitleFromContent(fortyNine)).toBe(fortyNine)
  })

  it('counts characters, never splitting an emoji', () => {
    // 25 emoji are 25 characters but 50 UTF-16 code units: the cut is counted
    // in runes, so half of a surrogate pair can never be kept.
    const emoji = '🐙'.repeat(60)
    const title = sessionTitleFromContent(emoji)
    expect(title).toBe(`${'🐙'.repeat(50)}...`)
    expect(Array.from(title)).toHaveLength(53)
  })

  it('has no temporary title for an empty message', () => {
    expect(sessionTitleFromContent('')).toBe('')
    expect(sessionTitleFromContent('   \n  ')).toBe('')
  })
})
