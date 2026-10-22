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

// The same failure from a provider that wrote no sentence of its own: the
// wording then comes from the locales, and the reset has to be counted down.
const usageLimitWithoutMessage = {
  code: 'rate_limit_quota',
  status: 429,
  plan: 'plus',
  resetAt: '2026-09-04T17:30:35Z',
  retryAfterSeconds: 8899,
}

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
  it('quotes the provider sentence with the status, in both languages', () => {
    const detail = parseProviderErrorDetail(usageLimitPayload)!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe(
      'The usage limit has been reached（HTTP 429）',
    )
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe(
      'The usage limit has been reached (HTTP 429)',
    )
    expect(formatProviderError(detail, 'en', arrivedAt)).not.toContain('\n')
    expect(formatProviderError(detail, 'en', arrivedAt)).not.toContain('usage_limit_reached')
  })

  it('counts down from the absolute reset rather than repeating the original wait', () => {
    const detail = parseProviderErrorDetail(usageLimitWithoutMessage)!
    const anHourLater = new Date(arrivedAt.getTime() + 60 * 60 * 1000)
    expect(formatProviderError(detail, 'en', anHourLater)).toContain('1h 28m')
  })

  it('says the allowance is back once the reset has passed', () => {
    const detail = parseProviderErrorDetail(usageLimitWithoutMessage)!
    const afterReset = new Date('2026-09-04T18:00:00Z')
    expect(formatProviderError(detail, 'zh', afterReset)).toContain('额度应该已经在')
  })

  it('gives a plain throttle as the provider sentence and the status', () => {
    const detail = parseProviderErrorDetail({
      code: 'rate_limit_throttle',
      status: 429,
      providerMessage: 'Rate limit reached for gpt-test on requests per min',
      retryAfterSeconds: 20,
    })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe(
      'Rate limit reached for gpt-test on requests per min（HTTP 429）',
    )
  })

  it('admits when the provider gave no reset', () => {
    const detail = parseProviderErrorDetail({ code: 'rate_limit_throttle', status: 429 })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toContain('没有说明什么时候恢复')
  })

  it('renders the non-rate-limit failures with the provider words and the status', () => {
    const detail = parseProviderErrorDetail({
      code: 'credentials',
      status: 401,
      providerMessage: 'Incorrect API key provided',
    })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe('Incorrect API key provided（HTTP 401）')
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe('Incorrect API key provided (HTTP 401)')
  })

  it('states the failure itself when the provider wrote nothing', () => {
    const detail = parseProviderErrorDetail({ code: 'credentials', status: 401 })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe('模型服务拒绝了本次请求的凭据。（HTTP 401）')
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe(
      "The provider rejected this request's credentials. (HTTP 401)",
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

  it('says a refused turn is waiting on an approval, in both languages', () => {
    // The runtime refused this turn before any provider was asked; the code
    // carries no status and quotes no provider, and the sentence says what
    // the reader does next.
    const detail = parseProviderErrorDetail({ code: 'session_awaiting_approval' })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe('这个对话正在等待审批，请先处理审批再发送。')
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe(
      'This conversation is waiting for an approval; answer it before sending another message.',
    )
  })

  it('says a refused turn is busy with a running one, in both languages', () => {
    // Another live run is driving the conversation — the sentence says what
    // happened and when the reader can try again, and nothing else.
    const detail = parseProviderErrorDetail({ code: 'session_running' })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe('这个对话正在运行一个回合，等它结束后再发送。')
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe(
      'This conversation is already running a turn; send again when it finishes.',
    )
  })

  it('names the other surface when the run driving the turn is not this one, in both languages', () => {
    // The live run belongs to another process — the other surface, or another
    // window of this one — so the sentence says where it is running rather
    // than leaving the reader to guess whether it is their own turn.
    const detail = parseProviderErrorDetail({ code: 'session_running_elsewhere' })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe('这个对话正在另一端运行（终端或网页），等它结束后再发送。')
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe(
      'This conversation is running on another surface (the terminal or the web); send again when it finishes there.',
    )
  })

  it('names the other surface holding an approval, in both languages', () => {
    // A run parked on an approval that another process is holding: the reader
    // is told the approval is elsewhere, not left holding the composer.
    const detail = parseProviderErrorDetail({ code: 'session_awaiting_approval_elsewhere' })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe('这个对话正在另一端等待审批（终端或网页），请在那一端处理后再发送。')
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe(
      'This conversation is waiting for an approval on another surface (the terminal or the web); answer it there before sending another message.',
    )
  })

  it('words a scheduled run\'s ending in one sentence, in both languages', () => {
    // A fire's record carries the runtime's English sentence as its fallback;
    // the code says which sentence the viewer reads, and the stored words are
    // not quoted for these — the sentence is the whole explanation.
    const cases = [
      ['run_failed', '这次运行失败了。', 'This run failed.'],
      ['run_stopped', '这次运行在完成前被停止了。', 'The run was stopped before it finished.'],
      ['run_abandoned', '运行这个回合的进程在它完成前停止了。', 'The process running this turn stopped before it finished.'],
      ['no_runtime', '调度器没有可用的运行环境。', 'No runtime is bound to the scheduler.'],
      ['no_delivery_channel', '没有可用于投递的渠道。', 'No delivery channel is bound.'],
    ] as const
    for (const [code, zh, en] of cases) {
      const detail = parseProviderErrorDetail({ code, providerMessage: 'the runtime sentence' })!
      expect(formatProviderError(detail, 'zh', arrivedAt)).toBe(zh)
      expect(formatProviderError(detail, 'en', arrivedAt)).toBe(en)
    }
  })

  it('quotes the channel\'s words under a failed delivery, in both languages', () => {
    const detail = parseProviderErrorDetail({
      code: 'delivery_failed',
      providerMessage: 'channel: no bound handler for channel id',
    })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe(
      '答复没能投递到渠道。\nchannel: no bound handler for channel id',
    )
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe(
      'The answer could not be delivered to the channel.\nchannel: no bound handler for channel id',
    )
    // Without words of its own, the sentence stands alone.
    const bare = parseProviderErrorDetail({ code: 'delivery_failed' })!
    expect(formatProviderError(bare, 'zh', arrivedAt)).toBe('答复没能投递到渠道。')
  })

  it('quotes the reason a scheduled run never started, in both languages', () => {
    const detail = parseProviderErrorDetail({ code: 'fire_start_failed', providerMessage: 'session store unavailable' })!
    expect(formatProviderError(detail, 'zh', arrivedAt)).toBe('这次定时任务没能开始运行。\nsession store unavailable')
    expect(formatProviderError(detail, 'en', arrivedAt)).toBe('This scheduled run could not start.\nsession store unavailable')
    const bare = parseProviderErrorDetail({ code: 'fire_start_failed' })!
    expect(formatProviderError(bare, 'en', arrivedAt)).toBe('This scheduled run could not start.')
  })

  it('returns null for a code this build does not know, so the caller keeps the runtime sentence', () => {
    const detail = parseProviderErrorDetail({ code: 'something_new', status: 418 })!
    expect(formatProviderError(detail, 'en', arrivedAt)).toBeNull()
  })

  it('follows the active locale when none is passed', () => {
    // A provider sentence is shown in the provider's language, so following the
    // viewer's language is only observable where forebrain writes the line.
    const detail = parseProviderErrorDetail(usageLimitWithoutMessage)!
    setLocale('zh')
    expect(formatProviderError(detail)).toContain('套餐用量已经用完')
    setLocale('en')
    expect(formatProviderError(detail)).toContain('Usage limit reached')
  })
})
