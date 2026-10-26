import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { setLocale } from '@/locales'
import ShellCardBody from './ShellCardBody.vue'
import type { ShellCard } from '@/composables/useChatStream'

config.global.directives = { tip }

function cardOf(overrides: Partial<ShellCard> = {}): ShellCard {
  return {
    stdout: '', stderr: '',
    stdoutOmittedBytes: 0, stderrOmittedBytes: 0, omittedBytes: 0,
    retrieveId: 0, spoolOmittedBytes: 0,
    files: [], notes: [],
    ...overrides,
  }
}

/** The body of a settled call, mounted the way ToolCallCard mounts it. */
function bodyOf(card: ShellCard | undefined, props: Record<string, unknown> = {}) {
  return mount(ShellCardBody, {
    props: { card, status: 'done', command: 'go test ./pkg/...', ...props },
  })
}

describe('ShellCardBody', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => setLocale('zh'))

  it('draws stdout one row a line and stderr in red', () => {
    const card = cardOf({ stdout: 'ok  pkg/tool\t0.5s\nPASS', stderr: 'fail pkg/gateway\nexit 1' })
    const wrapper = bodyOf(card)
    const rows = wrapper.findAll('.ol')
    expect(rows.map((row) => row.text())).toEqual(['ok  pkg/tool\t0.5s', 'PASS', 'fail pkg/gateway', 'exit 1'])
    expect(rows[2]!.classes()).toContain('err')
    expect(rows[3]!.classes()).toContain('err')
  })

  it('copies stdout alone, and stdout with stderr after it when both printed', async () => {
    const write = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText: write } })
    const stdoutOnly = bodyOf(cardOf({ stdout: 'only' }))
    await stdoutOnly.get('[data-testid="shell-copy"]').trigger('click')
    expect(write).toHaveBeenLastCalledWith('only')

    const both = bodyOf(cardOf({ stdout: 'out', stderr: 'err' }))
    await both.get('[data-testid="shell-copy"]').trigger('click')
    expect(write).toHaveBeenLastCalledWith('out\nerr')
    vi.unstubAllGlobals()
  })

  it('says 没有输出 when nothing printed and nothing changed', () => {
    const wrapper = bodyOf(cardOf())
    expect(wrapper.get('.none').text()).toBe('没有输出')
  })

  it('draws no output panel at all when nothing printed but files changed', () => {
    const wrapper = bodyOf(cardOf({ files: [{ verb: 'Created', path: 'a.go', added: 1, deleted: 0 }] }))
    expect(wrapper.find('.outz').exists()).toBe(false)
    expect(wrapper.text()).toContain('a.go')
  })

  it('names what the middle omitted and opens the whole output from the eye', async () => {
    const card = cardOf({ stdout: 'tail', stdoutOmittedBytes: 421888, retrieveId: 9 })
    const wrapper = mount(ShellCardBody, {
      props: {
        card, status: 'done', command: 'go test ./pkg/...\ncd sub && go test',
        docSource: { sessionId: 's-1' },
      },
    })
    const foot = wrapper.get('[data-testid="shell-foot"]')
    expect(foot.text()).toContain('中间 412 KB 已省略')
    const eye = wrapper.get('[data-testid="shell-open-output"]')
    expect(eye.attributes('aria-label')).toBe('查看完整输出')

    const opened = wrapper.emitted('open-doc')
    await eye.trigger('click')
    expect(wrapper.emitted('open-doc')).toHaveLength((opened?.length ?? 0) + 1)
    const emitted = wrapper.emitted('open-doc')!
    const tab = emitted[emitted.length - 1]![0] as Record<string, unknown>
    expect(tab.kind).toBe('output')
    expect(tab.label).toBe('输出 · go test ./pkg/...')
    expect(tab.command).toBe('go test ./pkg/...\ncd sub && go test')
    expect(tab.source).toEqual({ sessionId: 's-1', retrieveId: 9 })

    // The unnamed omission counts too; a subagent’s eye names its agent.
    const unnamed = bodyOf(cardOf({ omittedBytes: 3 * 1024 * 1024 + 400 * 1024, retrieveId: 0 }))
    expect(unnamed.get('[data-testid="shell-foot"]').text()).toContain('中间 3.4 MB 已省略')
    expect(unnamed.find('[data-testid="shell-open-output"]').exists()).toBe(false)
  })

  it('shows the last four running lines, the fold count above them and a caret at the end', () => {
    const liveOutput = Array.from({ length: 10 }, (_, i) => `line-${i + 1}`).join('\n')
    const wrapper = mount(ShellCardBody, {
      props: { card: undefined, status: 'running', command: 'go test', liveOutput },
    })
    expect(wrapper.get('.earlier').text()).toBe('前面 6 行已折叠')
    const rows = wrapper.findAll('.ol')
    expect(rows.map((row) => row.text())).toEqual(['line-7', 'line-8', 'line-9', 'line-10'])
    expect(rows[3]!.find('.caret').exists()).toBe(true)
    // A line still being written does not count as a finished one.
    const partial = mount(ShellCardBody, {
      props: { card: undefined, status: 'running', command: 'go test', liveOutput: 'one\ntwo\nthre' },
    })
    expect(partial.findAll('.ol').map((row) => row.text())).toEqual(['one', 'two', 'thre'])
    expect(partial.find('.earlier').exists()).toBe(false)
  })

  it('renders nothing from the card’s body Markdown — only the facts it carries', () => {
    const wrapper = bodyOf(cardOf({ stdout: 'ok' }))
    const text = wrapper.text()
    expect(text).not.toContain('**shell**')
    expect(text).not.toContain('input:')
    expect(text).not.toContain('output:')
    expect(text).not.toContain('stdout:')
  })
})
