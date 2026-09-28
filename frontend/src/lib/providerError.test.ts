import { describe, expect, it } from 'vitest'

import { formatProviderError, parseProviderErrorDetail } from './providerError'
import { setLocale } from '@/locales'

// The gateway's detail for a spent ChatGPT subscription allowance, after the
// websocket layer has camel-cased the keys.
const usageLimitPayload = {
  code: 'rate_limit_quota',
  status: 429,
  plan: 'plus',
  providerMessage: 'The usage limit has been reached',
  resetAt: '2026-09-04T17:30:35Z',
  retryAfterSeconds: 8899,
}

const arrivedAt = new Date('2026-09-04T15:02:16Z')

describe('provider error detail', () => {
  it('reads the gateway payload', () => {
    expect(parseProviderErrorDetail(usageLimitPayload)).toEqual({
      code: 'rate_limit_quota',
      status: 429,
      plan: 'plus',
      providerMessage: 'The usage limit has been reached',
      resetAt: '2026-09-04T17:30:35Z',
      retryAfterSeconds: 8899,
    })
  })

  it('rejects payloads without a code', () => {
    expect(parseProviderErrorDetail({ status: 429 })).toBeNull()
    expect(parseProviderErrorDetail(null)).toBeNull()
    expect(parseProviderErrorDetail('boom')).toBeNull()
  })

  it('drops empty and non-positive fields', () => {
    expect(parseProviderErrorDetail({ code: 'rejected', status: 0, plan: '  ' })).toEqual({
      code: 'rejected',
      status: undefined,
      plan: undefined,
      providerMessage: undefined,
      resetAt: undefined,
      retryAfterSeconds: undefined,
    })
  })
})

describe('provider error wording', () => {
  it('says what happened and when it recovers, in one Chinese sentence', () => {
    const detail = parseProviderErrorDetail(usageLimitPayload)!
    const text = formatProviderError(detail, 'zh', arrivedAt)!
    expect(text).toContain('你的 plus 套餐用量已经用完')
    expect(text).toContain('2 小时 28 分钟')
    expect(text).not.toContain('\n')
    expect(text).not.toContain('{')
    expect(text).not.toContain('usage_limit_reached')
  })

  it('says the same thing in one English sentence', () => {
    const detail = parseProviderErrorDetail(usageLimitPayload)!
    const text = formatProviderError(detail, 'en', arrivedAt)!
    expect(text).toContain('Usage limit reached on your plus plan')
    expect(text).toContain('available again in 2h 28m')
    expect(text).not.toContain('\n')
  })

  it('counts down from the absolute reset rather than repeating the original wait', () => {
    const detail = parseProviderErrorDetail(usageLimitPayload)!
    const anHourLater = new Date(arrivedAt.getTime() + 60 * 60 * 1000)
    expect(formatProviderError(detail, 'en', anHourLater)).toContain('1h 28m')
  })

  it('says the allowance is back once the reset has passed', () => {
    const detail = parseProviderErrorDetail(usageLimitPayload)!
    const afterReset = new Date('2026-09-04T18:00:00Z')
    expect(formatProviderError(detail, 'zh', afterReset)).toContain('额度应该已经在')
  })

  it('repeats the provider sentence for a plain throttle', () => {
    const detail = parseProviderErrorDetail({
      code: 'rate_limit_throttle',
      status: 429,
      providerMessage: 'Rate limit reached for gpt-test on requests per min',
      retryAfterSeconds: 20,
    })!
    const text = formatProviderError(detail, 'zh', arrivedAt)!
    expect(text.split('\n')).toEqual([
      '模型服务正在限流，预计 20 秒后恢复。',
      '模型服务原话：Rate limit reached for gpt-test on requests per min',
    ])
  })

  it('admits when the provider gave no reset', () => {
    const detail = parseProviderErrorDetail({ code: 'rate_limit_throttle', status: 429 })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toContain('没有说明什么时候恢复')
  })

  it('renders the non-rate-limit failures with the status and the provider words', () => {
    const detail = parseProviderErrorDetail({
      code: 'credentials',
      status: 401,
      providerMessage: 'Incorrect API key provided',
    })!
    const zh = formatProviderError(detail, 'zh', arrivedAt)!
    expect(zh).toContain('模型服务拒绝了本次请求的凭据')
    expect(zh).toContain('（HTTP 401）')
    expect(zh).toContain('模型服务原话：Incorrect API key provided')
    expect(formatProviderError(detail, 'en', arrivedAt)).toContain(
      "rejected this request's credentials",
    )
  })

  it('leads an unclassified rejection with the provider sentence', () => {
    const detail = parseProviderErrorDetail({
      code: 'rejected',
      status: 422,
      providerMessage: 'tool_choice is not supported for this model',
    })!
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe(
      'tool_choice is not supported for this model (HTTP 422)',
    )
  })

  it('returns null for a code this build does not know, so the caller keeps the runtime sentence', () => {
    const detail = parseProviderErrorDetail({ code: 'something_new', status: 418 })!
    expect(formatProviderError(detail, 'en', arrivedAt)).toBeNull()
  })

  it('follows the active locale when none is passed', () => {
    const detail = parseProviderErrorDetail(usageLimitPayload)!
    setLocale('zh')
    expect(formatProviderError(detail)).toContain('套餐用量已经用完')
    setLocale('en')
    expect(formatProviderError(detail)).toContain('Usage limit reached')
  })
})
