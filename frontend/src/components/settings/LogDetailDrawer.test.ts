import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import tip from '@/directives/tip'
import { forebrainApi, type LogExchange, type LogRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'

import LogDetailDrawer from './LogDetailDrawer.vue'

/**
 * One record's drawer (plan 288). What it pins: a record that is not a call
 * shows its own facts, error and stack; a call shows the four things a reader
 * asks of it, and each tab reads its own payload — once, and only when it is
 * opened.
 */

beforeAll(() => setLocale('zh'))
afterEach(() => {
  vi.restoreAllMocks()
  // A drawer is portalled to the body, and a case that fails before its own
  // unmount would otherwise leave a panel the next case's query finds first.
  document.body.innerHTML = ''
})

const AT = new Date(2026, 10, 11, 11, 20, 57, 545).toISOString()

function record(patch: Partial<LogRecord>): LogRecord {
  return {
    id: 'r1',
    day: '2026-11-11',
    source: 'tui',
    host: 'mbp-dev',
    pid: 4242,
    t: AT,
    lvl: 'INFO',
    kind: 'log',
    msg: 'agent load',
    ...patch,
  }
}

const CALL_RECORD = record({
  id: 'call-1',
  kind: 'llm',
  msg: 'llm response',
  session: 's1',
  sessionTitle: '修复页脚 token 统计',
  run: 'run-1',
  attrs: { x: 'gw-1-000001', status: 200, durMs: 1200 },
})

function exchange(patch: Partial<LogExchange> = {}): LogExchange {
  return {
    id: 'gw-1-000001',
    day: '2026-11-11',
    request: {
      t: AT,
      method: 'POST',
      url: 'https://api.example.com/v1/chat',
      model: 'glm-5.3',
      bytes: 120,
      whole: true,
      blob: 'gw-1-000001.req.gz',
    },
    response: {
      t: AT,
      status: 200,
      durMs: 1200,
      ttfbMs: 400,
      maxGapMs: 200,
      bytes: 2048,
      blob: 'gw-1-000001.resp.gz',
    },
    scope: { source: 'tui', host: 'mbp-dev', pid: 4242, session: 's1', sessionTitle: '修复页脚 token 统计', run: 'run-1', agent: 'main' },
    summary: {
      text: '改好了：切换模型后立即用新窗口重算占用。',
      reasoning: '页脚的 token 数来自 budget 事件。',
      toolCalls: [{ name: 'shell', arguments: '{"command":"rg -n Budget pkg/run"}' }],
      finishReason: 'toolCalls',
      usage: { input: 1200, output: 96 },
      events: 42,
    },
    concurrent: [],
    ...patch,
  }
}

function mountDrawer(record_: LogRecord): VueWrapper {
  return mount(LogDetailDrawer, {
    props: { open: true, record: record_ },
    attachTo: document.body,
    global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
}

function panel(testid: string): HTMLElement | null {
  return document.querySelector(`[data-testid="${testid}"]`)
}

async function openTab(id: string): Promise<void> {
  panel(id)?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await flushPromises()
}

describe('LogDetailDrawer', () => {
  it('draws a plain record’s facts, its error and its stack', async () => {
    const wrapper = mountDrawer(
      record({
        kind: 'panic',
        lvl: 'ERROR',
        msg: 'recovered: run.dispatchSubagent',
        err: 'runtime error: index out of range [3] with length 3',
        session: 's1',
        sessionTitle: '修复页脚 token 统计',
        run: 'run-1',
        at: 'pkg/run/subagent.go:4123',
        attrs: { stack: 'goroutine 412 [running]:\nrun.dispatchSubagent()' },
      }),
    )
    await flushPromises()

    expect(panel('log-drawer-lead')?.textContent).toContain('recovered: run.dispatchSubagent')
    expect(panel('log-drawer-error')?.textContent).toContain('index out of range [3] with length 3')
    expect(panel('log-drawer-stack')?.textContent).toContain('goroutine 412')
    expect(panel('log-drawer-attrs')?.textContent).toContain('goroutine 412')
    expect(panel('log-drawer-only-session')).not.toBeNull()
    expect(panel('log-drawer-only-run')).not.toBeNull()
    expect(document.body.textContent).toContain('pkg/run/subagent.go:4123')
    // No call of its own: no model-call view was opened.
    expect(panel('log-drawer-tabs')).toBeNull()
    wrapper.unmount()
  })

  it('opens a call on its overview, and reads each payload only when its tab is opened', async () => {
    const call = vi.spyOn(forebrainApi, 'logExchange').mockResolvedValue(exchange())
    const blob = vi.spyOn(forebrainApi, 'logBlob').mockImplementation(async (_day, name) =>
      name.includes('.req')
        ? {
            text: 'POST https://api.example.com/v1/chat HTTP/1.1\nContent-Type: application/json\n\n{"model":"glm-5.3","messages":[]}',
            total: 76,
          }
        : { text: 'HTTP/2.0 200 OK\ncontent-type: text/event-stream\n\ndata: {"a":1}\ndata: [DONE]', total: 71 },
    )
    const wrapper = mountDrawer(CALL_RECORD)
    await flushPromises()

    expect(call).toHaveBeenCalledWith('2026-11-11', 'gw-1-000001')
    expect(panel('log-drawer-overview')).not.toBeNull()
    expect(panel('log-drawer-badge')?.textContent).toBe('200')
    expect(document.body.textContent).toContain('1.2s')
    expect(document.body.textContent).toContain('400ms')
    // The overview reads no payload at all.
    expect(blob).not.toHaveBeenCalled()

    // 请求: the request payload, once.
    await openTab('log-drawer-tab-req')
    expect(blob).toHaveBeenCalledTimes(1)
    expect(blob).toHaveBeenLastCalledWith('2026-11-11', 'gw-1-000001.req.gz', 0, 256 * 1024)
    expect(panel('log-drawer-request-head')?.textContent).toContain('POST https://api.example.com/v1/chat')
    expect(panel('log-drawer-request-download')?.getAttribute('href')).toContain('download=1')

    // 响应: the summary the exchange already carried — no second read.
    await openTab('log-drawer-tab-resp')
    expect(blob).toHaveBeenCalledTimes(1)
    expect(panel('log-drawer-answer')?.textContent).toContain('改好了')
    expect(panel('log-drawer-thinking')?.textContent).toContain('budget 事件')
    expect(panel('log-drawer-tool-call')?.textContent).toContain('shell')

    // 原文: the response payload, once, numbered as the file wrote it.
    await openTab('log-drawer-tab-raw')
    expect(blob).toHaveBeenCalledTimes(2)
    expect(panel('log-drawer-raw-rows')?.textContent).toContain('data: [DONE]')

    // Back to 请求: the payload it already read is not read again.
    await openTab('log-drawer-tab-req')
    expect(blob).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('says a call that has not ended has no response, and still shows its request', async () => {
    vi.spyOn(forebrainApi, 'logExchange').mockResolvedValue(
      exchange({ response: null, summary: null, request: { ...exchange().request!, whole: false, omitted: 65536 } }),
    )
    const blob = vi
      .spyOn(forebrainApi, 'logBlob')
      .mockResolvedValue({ text: 'POST https://api.example.com/v1/chat HTTP/1.1\n\n…省略…', total: 120 })
    const wrapper = mountDrawer(CALL_RECORD)
    await flushPromises()

    await openTab('log-drawer-tab-resp')
    expect(panel('log-drawer-no-response')?.textContent).toContain('这次调用还没有结束，或进程在结束前退出')
    expect(panel('log-drawer-see-request')).not.toBeNull()

    await openTab('log-drawer-tab-req')
    expect(blob).toHaveBeenCalledTimes(1)
    // A trimmed request says how much was left out, in the prototype's warn bar.
    expect(panel('log-drawer-request-cut')?.textContent).toContain('成功的调用只留头尾各 32KB，中间省略 64.0KB')
    wrapper.unmount()
  })

  it('keeps a failed call’s request whole, and says so', async () => {
    vi.spyOn(forebrainApi, 'logExchange').mockResolvedValue(
      exchange({
        request: { ...exchange().request!, bytes: 1_176_204, whole: true },
        response: { t: AT, status: 400, durMs: 5195, ttfbMs: 5190, maxGapMs: 5190, bytes: 156, blob: 'gw-1-000001.resp.gz' },
        summary: null,
      }),
    )
    const blob = vi
      .spyOn(forebrainApi, 'logBlob')
      .mockImplementation(async (_day, name) =>
        name.includes('.req')
          ? { text: 'POST https://api.example.com/v1/chat HTTP/1.1\n\n{"model":"glm-5.3"}', total: 1_176_204 }
          : { text: 'HTTP/2.0 400 Bad Request\ncontent-type: application/json\n\n{"error":{"message":"invalid argument"}}', total: 156 },
      )
    const wrapper = mountDrawer(CALL_RECORD)
    await flushPromises()

    await openTab('log-drawer-tab-req')
    expect(panel('log-drawer-request-whole')?.textContent).toContain('这次调用失败，请求体完整保留（1.12MB）。')

    // A refused call has no summary: its response tab says the upstream's words.
    await openTab('log-drawer-tab-resp')
    expect(panel('log-drawer-response-head')?.textContent).toContain('400 Bad Request')
    expect(panel('log-drawer-response-body')?.textContent).toContain('invalid argument')
    expect(blob).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('follows the strip into another call', async () => {
    const call = vi
      .spyOn(forebrainApi, 'logExchange')
      .mockResolvedValueOnce(
        exchange({
          concurrent: [
            {
              id: 'gw-1-000002',
              day: '2026-11-11',
              session: 's2',
              agent: 'plan-reviewer',
              t0: AT,
              t1: AT,
              status: 200,
              sessionTitle: 'Gemini 工具循环 400 排查',
            },
          ],
        }),
      )
      .mockResolvedValueOnce(exchange({ id: 'gw-1-000002', scope: { ...exchange().scope, session: 's2', sessionTitle: 'Gemini 工具循环 400 排查' } }))
    vi.spyOn(forebrainApi, 'logBlob').mockResolvedValue({ text: '', total: 0 })
    const wrapper = mountDrawer(CALL_RECORD)
    await flushPromises()

    expect(document.body.textContent).toContain('1 次，各归各的会话')
    const lane = panel('log-drawer-lane-gw-1-000002')
    expect(lane).not.toBeNull()
    lane?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()

    expect(call).toHaveBeenLastCalledWith('2026-11-11', 'gw-1-000002')
    expect(panel('log-drawer-title')?.textContent).toContain('Gemini 工具循环 400 排查')
    wrapper.unmount()
  })
})
