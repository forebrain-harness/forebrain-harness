import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { setLocale } from '@/locales'
import { forebrainApi } from '@/lib/api'
import ToolDocOutput from './ToolDocOutput.vue'

config.global.directives = { tip }

/** One observer the page can fire at will, the way the browser would. */
class FakeIntersectionObserver implements IntersectionObserver {
  readonly root = null
  readonly rootMargin = ''
  readonly thresholds: ReadonlyArray<number> = []
  static instances: FakeIntersectionObserver[] = []
  private callback: IntersectionObserverCallback
  private targets = new Set<Element>()
  constructor(callback: IntersectionObserverCallback) {
    this.callback = callback
    FakeIntersectionObserver.instances.push(this)
  }
  observe(target: Element): void {
    this.targets.add(target)
  }
  unobserve(target: Element): void {
    this.targets.delete(target)
  }
  disconnect(): void {
    this.targets.clear()
  }
  takeRecords(): IntersectionObserverEntry[] {
    return []
  }
  /** The reader reached the bottom sentinel. */
  intersect(): void {
    const entries = [...this.targets].map((target) => ({ target, isIntersecting: true })) as unknown as IntersectionObserverEntry[]
    this.callback(entries, this as unknown as IntersectionObserver)
  }
}

function mountOutput(source: { sessionId: string; agentId?: string; retrieveId: number }) {
  return mount(ToolDocOutput, { props: { source } })
}

describe('ToolDocOutput', () => {
  beforeAll(() => {
    setLocale('zh')
    vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver)
  })
  afterEach(() => {
    vi.restoreAllMocks()
    FakeIntersectionObserver.instances = []
    setLocale('zh')
  })

  it('reads the conversation’s own spool and lays the whole text out', async () => {
    const spy = vi.spyOn(forebrainApi, 'chatToolOutput').mockResolvedValue({
      command: 'go test ./pkg/...',
      text: 'ok  pkg/tool\t0.4s\nok  pkg/gateway\t1.2s',
      spoolOmittedBytes: 0,
    })
    const wrapper = mountOutput({ sessionId: 's-1', retrieveId: 3 })
    await vi.waitFor(() => expect(wrapper.find('pre').exists()).toBe(true))
    expect(spy).toHaveBeenCalledWith('s-1', 3)
    expect(wrapper.get('pre').text()).toBe('ok  pkg/tool\t0.4s\nok  pkg/gateway\t1.2s')
    expect(wrapper.find('.spool-note').exists()).toBe(false)
  })

  it('reads a subagent’s spool through its own route', async () => {
    const subagent = vi.spyOn(forebrainApi, 'subagentToolOutput').mockResolvedValue({
      command: 'ls', text: 'a.go', spoolOmittedBytes: 0,
    })
    const plain = vi.spyOn(forebrainApi, 'chatToolOutput')
    const wrapper = mountOutput({ sessionId: 's-1', agentId: 'agent-7', retrieveId: 5 })
    await vi.waitFor(() => expect(wrapper.find('pre').exists()).toBe(true))
    expect(subagent).toHaveBeenCalledWith('s-1', 'agent-7', 5)
    expect(plain).not.toHaveBeenCalled()
  })

  it('says the output is gone on a 404, and the error otherwise', async () => {
    vi.spyOn(forebrainApi, 'chatToolOutput').mockRejectedValue(
      Object.assign(new Error('not found'), { response: { status: 404 } }),
    )
    const gone = mountOutput({ sessionId: 's-1', retrieveId: 9 })
    await vi.waitFor(() => expect(gone.text()).toContain('完整输出已不在了'))
    expect(gone.find('pre').exists()).toBe(false)

    vi.spyOn(forebrainApi, 'chatToolOutput').mockRejectedValue(
      Object.assign(new Error('boom'), { response: { status: 500, data: 'boom' } }),
    )
    const failed = mountOutput({ sessionId: 's-1', retrieveId: 10 })
    await vi.waitFor(() => expect(failed.text()).toContain('boom'))
    expect(failed.find('.state--error').exists()).toBe(true)
  })

  it('renders 2 MB at a time and appends the next slice when the sentinel shows', async () => {
    const chunk = 'x'.repeat(1024)
    const total = 3 * 1024 + 1 // lines of 1025 bytes: a little over 3 MB
    vi.spyOn(forebrainApi, 'chatToolOutput').mockResolvedValue({
      command: 'cat big',
      text: `${chunk}\n`.repeat(total),
      spoolOmittedBytes: 0,
    })
    const wrapper = mountOutput({ sessionId: 's-1', retrieveId: 4 })
    await vi.waitFor(() => expect(wrapper.find('pre').exists()).toBe(true))
    const pre = wrapper.get('pre').element as HTMLElement
    // The first slice is the leading 2 MB exactly — the cut is by bytes.
    expect(pre.textContent?.length).toBe(2 * 1024 * 1024)

    const observers = FakeIntersectionObserver.instances
    const observer = observers[observers.length - 1]!
    observer.intersect()
    await vi.waitFor(() => expect((wrapper.get('pre').element as HTMLElement).textContent?.length).toBe(total * 1025))
    // The whole text is what copy gives, never the slice drawn.
    expect(wrapper.getComponent({ name: 'DocCopyButton' }).props('text').length).toBe(total * 1025)
  })

  it('notes what the spool never kept past its cap', async () => {
    vi.spyOn(forebrainApi, 'chatToolOutput').mockResolvedValue({
      command: 'cat huge', text: '…', spoolOmittedBytes: 72 * 1024 * 1024,
    })
    const wrapper = mountOutput({ sessionId: 's-1', retrieveId: 6 })
    await vi.waitFor(() => expect(wrapper.find('.spool-note').exists()).toBe(true))
    expect(wrapper.get('.spool-note').text()).toBe('超过 64 MB 的部分没有保存')
  })
})
