import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { config, mount, type VueWrapper } from '@vue/test-utils'
import tip from '@/directives/tip'

import ChatWorkbench from './ChatWorkbench.vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

function stubApi() {
  vi.spyOn(forebrainApi, 'sessionTodos').mockResolvedValue({ items: [] })
  vi.spyOn(forebrainApi, 'sessionPlanMd').mockResolvedValue({ markdown: '' })
  vi.spyOn(forebrainApi, 'mcpServersWithStatus').mockResolvedValue({
    servers: [],
    project: {},
    runtimeScope: undefined,
    runtimeAvailable: false,
    generation: undefined,
    pending: false,
  })
  vi.spyOn(forebrainApi, 'sessionCostSummary').mockResolvedValue({
    sessionId: 's1',
    inputTokens: 0,
    outputTokens: 0,
    cacheRead: 0,
    cacheWritten: 0,
    uncached: 0,
    requests: 0,
    cacheHitPercent: 0,
    toolCalls: 0,
    byTool: {},
  })
  vi.spyOn(forebrainApi, 'sessionContext').mockResolvedValue({})
  vi.spyOn(forebrainApi, 'sessionFileChanges').mockResolvedValue([])
  vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: [] })
}

function mountBench(): VueWrapper {
  return mount(ChatWorkbench, {
    props: { records: [], loading: false, error: '', overlay: false, scope: { sessionId: 's1' } },
    global: { stubs: { teleport: true } },
  })
}

describe('ChatWorkbench', () => {
  beforeEach(() => {
    localStorage.clear()
    stubApi()
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('shows the four tabs and defaults to overview', async () => {
    const wrapper = mountBench()
    await flush()
    const tabs = wrapper.findAll('[role="tab"]')
    expect(tabs.map((tab) => tab.text())).toEqual(['概览', '上下文', '工作目录', '变更文件'])
    expect(wrapper.find('[data-testid="bench-tab-overview"]').attributes('aria-selected')).toBe('true')
  })

  it('switches tabs and remembers the last one per conversation', async () => {
    const first = mountBench()
    await flush()
    await first.find('[data-testid="bench-tab-tree"]').trigger('click')
    await flush()
    expect(first.find('[data-testid="bench-tab-tree"]').attributes('aria-selected')).toBe('true')
    first.unmount()

    // A fresh mount of the same conversation opens the tab it was left on.
    const second = mountBench()
    await flush()
    expect(second.find('[data-testid="bench-tab-tree"]').attributes('aria-selected')).toBe('true')
    second.unmount()

    // A different conversation starts at the default again.
    const other = mount(ChatWorkbench, {
      props: { records: [], loading: false, error: '', overlay: false, scope: { sessionId: 's2' } },
      global: { stubs: { teleport: true } },
    })
    await flush()
    expect(other.find('[data-testid="bench-tab-overview"]').attributes('aria-selected')).toBe('true')
  })

  it('adds extra tabs and keeps a disabled one unselectable', async () => {
    const wrapper = mount(ChatWorkbench, {
      props: {
        records: [], loading: false, error: '', overlay: false, scope: { sessionId: 's3' },
        extraTabs: [
          { key: 'cases', label: 'workshop.tabCases', disabled: true },
          { key: 'versions', label: 'workshop.tabVersions' },
        ],
      },
      global: { stubs: { teleport: true } },
    })
    await flush()
    const tabs = wrapper.findAll('[role="tab"]')
    expect(tabs.map((tab) => tab.text())).toEqual(['概览', '上下文', '工作目录', '变更文件', '测试用例', '版本'])
    // A disabled tab does not become active when clicked.
    await wrapper.find('[data-testid="bench-tab-cases"]').trigger('click')
    expect(wrapper.find('[data-testid="bench-tab-cases"]').attributes('aria-selected')).toBe('false')
    await wrapper.find('[data-testid="bench-tab-versions"]').trigger('click')
    expect(wrapper.find('[data-testid="bench-tab-versions"]').attributes('aria-selected')).toBe('true')
  })
})
