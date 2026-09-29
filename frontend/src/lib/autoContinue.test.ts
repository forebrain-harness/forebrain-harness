import { describe, expect, it } from 'vitest'

import { formatAutoContinueNotice, formatAutoContinueTime, parseAutoContinue } from './autoContinue'
import { parseForebrainSessionBoundMessage } from './forebrainGatewayRuntime'

describe('auto-continue', () => {
  it('reads a scheduled continuation in either key spelling', () => {
    const snake = parseAutoContinue({
      continue_at: '2026-09-30T02:10:05Z',
      reset_at: '2026-09-30T02:10:00Z',
      code: 'rate_limit_quota',
      plan: 'pro',
      attempt: 2,
    })
    expect(snake).toEqual({
      continueAt: '2026-09-30T02:10:05Z',
      resetAt: '2026-09-30T02:10:00Z',
      code: 'rate_limit_quota',
      plan: 'pro',
      attempt: 2,
    })
    expect(parseAutoContinue({ continueAt: '2026-09-30T02:10:05Z', code: 'rate_limit_throttle' }))
      .toMatchObject({ continueAt: '2026-09-30T02:10:05Z', code: 'rate_limit_throttle' })
  })

  it('is no wait without a readable continuation time', () => {
    expect(parseAutoContinue(null)).toBeNull()
    expect(parseAutoContinue({ code: 'rate_limit_quota' })).toBeNull()
    expect(parseAutoContinue({ continue_at: 'soon' })).toBeNull()
  })

  it('says what stopped the conversation and when it picks up, in the viewer language', () => {
    const now = new Date(2026, 8, 29, 23, 0)
    const at = new Date(2026, 8, 29, 23, 40)
    const state = { continueAt: at.toISOString(), code: 'rate_limit_quota' }
    const clockEn = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit' }).format(at)
    const clockZh = new Intl.DateTimeFormat('zh-CN', { hour: 'numeric', minute: '2-digit' }).format(at)
    expect(formatAutoContinueNotice(state, 'en', now)).toBe(`Usage limit reached · continuing automatically at ${clockEn}`)
    expect(formatAutoContinueNotice(state, 'zh', now)).toBe(`用量已达上限 · 将在 ${clockZh} 自动继续`)
    expect(formatAutoContinueNotice({ ...state, code: 'rate_limit_throttle' }, 'en', now))
      .toBe(`Rate limited · continuing automatically at ${clockEn}`)
  })

  it('names the day only when the continuation is not today', () => {
    const now = new Date(2026, 8, 29, 23, 0)
    const today = formatAutoContinueTime(new Date(2026, 8, 29, 23, 40), 'en', now)
    const tomorrow = formatAutoContinueTime(new Date(2026, 8, 30, 2, 10), 'en', now)
    const later = formatAutoContinueTime(new Date(2026, 9, 9, 9, 0), 'en', now)
    expect(today).not.toMatch(/Wed|Oct/)
    expect(tomorrow).toMatch(/^Wed/)
    expect(later).toMatch(/Oct/)
    expect(later).not.toMatch(/2026/)
    expect(formatAutoContinueTime(new Date(2027, 0, 2, 9, 0), 'en', now)).toMatch(/2027/)
  })

  it('carries the pending continuation on a session binding', () => {
    const bound = parseForebrainSessionBoundMessage({
      op: 'session_bound',
      request_id: 'bind',
      session_id: 's1',
      message: 'subscribed',
      data: {
        cursor: 0,
        high_water: 42,
        schema_version: 1,
        auto_continue: { continue_at: '2026-09-30T02:10:05Z', code: 'rate_limit_quota', attempt: 1 },
      },
    })
    expect(bound).toMatchObject({
      sessionId: 's1',
      highWater: 42,
      autoContinue: { continueAt: '2026-09-30T02:10:05Z', code: 'rate_limit_quota', attempt: 1 },
    })
  })
})
