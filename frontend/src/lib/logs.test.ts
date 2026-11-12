import { describe, expect, it } from 'vitest'

import {
  attrNumber,
  levelCountsOf,
  attrText,
  defaultFilters,
  formatBytes,
  formatDuration,
  formatLogClock,
  formatLogDay,
  formatLogFull,
  formatLogShort,
  looksLikeJSON,
  rangeOf,
  rowKind,
  rfc3339,
  shortURL,
  splitPayload,
  toQuery,
  type LogFilters,
} from './logs'

/**
 * The log page's pure half (plan 288): what a preset covers, what a filter
 * change asks the gateway for, and how a record's row reads. The page itself
 * owns only state and rendering, so these are the tests that pin its behaviour.
 */

const NOW = Date.UTC(2026, 10, 11, 3, 52, 0)

/** A moment's own local spelling, built the way the page builds it. */
function localClock(year: number, month: number, day: number, hour: number, minute: number, second: number, ms = 0): string {
  return new Date(year, month - 1, day, hour, minute, second, ms).toISOString()
}

function filters(patch: Partial<LogFilters> = {}): LogFilters {
  return { ...defaultFilters(NOW), ...patch }
}

describe('rangeOf', () => {
  it('covers each preset from now backwards', () => {
    expect(rangeOf('15m', NOW, { from: 0, to: 0 })).toEqual({ from: NOW - 15 * 60_000, to: NOW })
    expect(rangeOf('1h', NOW, { from: 0, to: 0 })).toEqual({ from: NOW - 60 * 60_000, to: NOW })
    expect(rangeOf('7d', NOW, { from: 0, to: 0 })).toEqual({ from: NOW - 7 * 24 * 60 * 60_000, to: NOW })
  })

  it('starts today at the reader’s own midnight', () => {
    const midnight = new Date(2026, 10, 11, 0, 0, 0, 0).getTime()
    expect(rangeOf('today', NOW, { from: 0, to: 0 })).toEqual({ from: midnight, to: NOW })
  })

  it('returns a custom range exactly as it was chosen', () => {
    const custom = { from: NOW - 1000, to: NOW - 500 }
    expect(rangeOf('custom', NOW, custom)).toBe(custom)
  })
})

describe('toQuery', () => {
  it('carries the default filters: an hour, the loud three, the first page', () => {
    const query = toQuery(filters(), 1, 50, NOW)
    expect(query).not.toBeNull()
    expect(query?.level).toBe('INFO,WARN,ERROR')
    expect(query?.from).toBe(rfc3339(NOW - 60 * 60_000))
    expect(query?.to).toBe(rfc3339(NOW))
    expect(query?.page).toBe(1)
    expect(query?.page_size).toBe(50)
    // Nothing the reader did not narrow by is sent at all.
    expect(query?.source).toBeUndefined()
    expect(query?.project).toBeUndefined()
    expect(query?.session).toBeUndefined()
    expect(query?.kind).toBeUndefined()
    expect(query?.q).toBeUndefined()
  })

  it('joins the levels loudly first, whatever order they were switched on', () => {
    const query = toQuery(filters({ levels: ['ERROR', 'DEBUG', 'WARN'] }), 1, 20, NOW)
    expect(query?.level).toBe('DEBUG,WARN,ERROR')
  })

  it('sends each dimension the reader narrowed by', () => {
    const query = toQuery(
      filters({ source: 'gateway', project: 'p1', session: 's1', kind: 'llm', q: '  boom  ' }),
      3,
      10,
      NOW,
    )
    expect(query).toMatchObject({
      source: 'gateway',
      project: 'p1',
      session: 's1',
      kind: 'llm',
      q: 'boom',
      page: 3,
      page_size: 10,
    })
  })

  it('sends nothing when every level is switched off: an empty table is the page’s own answer', () => {
    expect(toQuery(filters({ levels: [] }), 1, 50, NOW)).toBeNull()
  })

  it('carries a custom range as the reader set it', () => {
    const query = toQuery(filters({ preset: 'custom', from: NOW - 90_000, to: NOW - 30_000 }), 1, 50, NOW)
    expect(query?.from).toBe(rfc3339(NOW - 90_000))
    expect(query?.to).toBe(rfc3339(NOW - 30_000))
  })
})

describe('levelCountsOf', () => {
  it('reads the lowercased tally the response pass leaves, in the record vocabulary', () => {
    // `{DEBUG: 9}` on the wire would be a different map: every response's keys
    // are camelCased, and a level name is a key.
    expect(levelCountsOf({ debug: 9, info: 2, warn: 1, error: 0 })).toEqual({ DEBUG: 9, INFO: 2, WARN: 1, ERROR: 0 })
    expect(levelCountsOf(undefined)).toEqual({ DEBUG: 0, INFO: 0, WARN: 0, ERROR: 0 })
  })
})

describe('rowKind', () => {
  it('tells a model call’s two records apart, and names every other kind', () => {
    expect(rowKind({ kind: 'llm', msg: 'llm request' })).toBe('llm-request')
    expect(rowKind({ kind: 'llm', msg: 'llm response' })).toBe('llm-response')
    expect(rowKind({ kind: 'tool', msg: 'tool result' })).toBe('tool')
    expect(rowKind({ kind: 'http', msg: 'request' })).toBe('http')
    expect(rowKind({ kind: 'turn', msg: '轮次完成' })).toBe('plain')
    // A record kind this page was not written for still draws as text.
    expect(rowKind({ kind: 'setup', msg: 'ready' })).toBe('plain')
  })
})

describe('formatDuration', () => {
  it('reads milliseconds, seconds and minutes the way the design writes them', () => {
    expect(formatDuration(0)).toBe('0ms')
    expect(formatDuration(858)).toBe('858ms')
    expect(formatDuration(31_900)).toBe('31.9s')
    expect(formatDuration(999)).toBe('999ms')
    expect(formatDuration(59_999)).toBe('60.0s')
    expect(formatDuration(814_000)).toBe('13m34s')
    expect(formatDuration(60_000)).toBe('1m00s')
  })

  it('keeps an unknown duration from reading as a number', () => {
    expect(formatDuration(Number.NaN)).toBe('—')
  })
})

describe('formatBytes', () => {
  it('reads bytes, kilobytes and megabytes the way the design writes them', () => {
    expect(formatBytes(0)).toBe('0B')
    expect(formatBytes(156)).toBe('156B')
    expect(formatBytes(1023)).toBe('1023B')
    expect(formatBytes(1024)).toBe('1.0KB')
    expect(formatBytes(95_232)).toBe('93.0KB')
    expect(formatBytes(1_174_405)).toBe('1.12MB')
  })
})

describe('the moment a row shows', () => {
  const at = localClock(2026, 11, 11, 11, 20, 57, 545)

  it('draws the clock to the millisecond and the day as the month and day', () => {
    expect(formatLogClock(at)).toBe('11:20:57.545')
    expect(formatLogDay(at)).toBe('11-11')
    expect(formatLogFull(at)).toBe('2026-11-11 11:20:57')
    expect(formatLogShort(at)).toBe('11-11 11:20')
    expect(formatLogShort(at, false)).toBe('11:20')
  })

  it('draws a moment it cannot parse as an em dash rather than NaN', () => {
    expect(formatLogClock('not a time')).toBe('—')
    expect(formatLogFull('')).toBe('—')
  })
})

describe('shortURL', () => {
  it('keeps the host and the path, and says a broken address as it is', () => {
    expect(shortURL('https://open.bigmodel.cn/api/coding/paas/v4/chat/completions?x=1')).toBe(
      'open.bigmodel.cn/api/coding/paas/v4/chat/completions',
    )
    expect(shortURL('not a url')).toBe('not a url')
  })
})

describe('payloads', () => {
  it('splits a payload into the head it starts with and the body after the blank line', () => {
    const text = 'POST https://api.example.com/v1/chat HTTP/1.1\nContent-Type: application/json\n\n{"model":"x"}'
    expect(splitPayload(text)).toEqual({
      head: 'POST https://api.example.com/v1/chat HTTP/1.1\nContent-Type: application/json',
      body: '{"model":"x"}',
    })
    expect(splitPayload('{"only":"body"}')).toEqual({ head: '', body: '{"only":"body"}' })
  })

  it('treats only a complete JSON document as one', () => {
    expect(looksLikeJSON('{"model":"x"}')).toBe(true)
    expect(looksLikeJSON('[1,2]')).toBe(true)
    // A request trimmed to head and tail is not a document any more.
    expect(looksLikeJSON('{"model":"x"}\n…省略…\n"tail":1}')).toBe(false)
    expect(looksLikeJSON('data: {"delta":"x"}')).toBe(false)
  })
})

describe('attrText and attrNumber', () => {
  const record = { attrs: { url: 'https://a/b', status: 400, durMs: '1200', stream: true } }

  it('reads a field as text, and an absent one as nothing', () => {
    expect(attrText(record, 'url')).toBe('https://a/b')
    expect(attrText(record, 'status')).toBe('400')
    expect(attrText(record, 'missing')).toBe('')
  })

  it('reads a number only where one is there', () => {
    expect(attrNumber(record, 'status')).toBe(400)
    expect(attrNumber(record, 'durMs')).toBe(1200)
    expect(attrNumber(record, 'stream')).toBe(0)
    expect(attrNumber(record, 'missing')).toBe(0)
  })
})

describe('rfc3339', () => {
  it('writes the second the gateway parses', () => {
    expect(rfc3339(Date.UTC(2026, 10, 11, 3, 52, 0, 999))).toBe('2026-11-11T03:52:00Z')
  })
})
