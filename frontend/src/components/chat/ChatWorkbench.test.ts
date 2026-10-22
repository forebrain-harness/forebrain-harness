import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { config, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
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
  vi.spyOn(forebrainApi, 'sessionPlans').mockResolvedValue([])
  vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([])
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

// The workshop's own four tabs, which make the conversation page's four an
// eight-tab bar — the case the arrows exist for.
const workshopTabs = [
  { key: 'cases', label: 'workshop.tabCases' as const },
  { key: 'evals', label: 'workshop.tabEvals' as const },
  { key: 'trigger', label: 'workshop.tabTrigger' as const },
  { key: 'versions', label: 'workshop.tabVersions' as const },
]

function mountWorkshopBench(): VueWrapper {
  return mount(ChatWorkbench, {
    props: {
      records: [], loading: false, error: '', overlay: false, scope: { sessionId: 'ws' },
      extraTabs: workshopTabs,
    },
    global: { stubs: { teleport: true } },
  })
}

function tabTexts(wrapper: VueWrapper): string[] {
  return wrapper.findAll('[role="tab"]').map((tab) => tab.text())
}

// jsdom lays nothing out, so the measurement strip's answer is supplied here the
// way the browser would answer it: the bar is `barWidth` wide and every label
// wants `tabWidth` — one width for every label, or one per label in order. The
// two calls the window is measured with, and nothing else: a `.bench-tabs`
// reports the bar's width, a `.bench-tab-btn` reports its own.
function stubLayout(tabWidth: number | number[], barWidth = 320) {
  vi.spyOn(Element.prototype, 'clientWidth', 'get').mockImplementation(function (this: Element) {
    return this.classList.contains('bench-tabs') ? barWidth : 0
  })
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    let width = 0
    if (this.classList.contains('bench-tab-btn')) {
      if (Array.isArray(tabWidth)) {
        width = tabWidth[Array.from(this.parentElement?.children ?? []).indexOf(this)] ?? 0
      } else {
        width = tabWidth
      }
    }
    return { x: 0, y: 0, top: 0, left: 0, right: width, bottom: 0, width, height: 0, toJSON: () => ({}) } as DOMRect
  })
}

// 概览 is 37px wide and 测试用例 is 62px in the Chinese column; Overview is 65px
// and Trigger description is 122 in the English one. Two label widths stand for
// the two locales: a bar of 320px holds 5.5 of the narrow ones and 3.9 of the
// wide ones.
const NARROW = 52
const WIDE = 82

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
    stubLayout(NARROW)
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
    // Six tabs, five at a time: the disabled case tab is in the first window.
    expect(tabTexts(wrapper)).toEqual(['概览', '上下文', '工作目录', '变更文件', '测试用例'])
    // A disabled tab does not become active when clicked.
    await wrapper.find('[data-testid="bench-tab-cases"]').trigger('click')
    expect(wrapper.find('[data-testid="bench-tab-cases"]').attributes('aria-selected')).toBe('false')
    await wrapper.find('[data-testid="bench-tabs-next"]').trigger('click')
    await flush()
    await wrapper.find('[data-testid="bench-tab-versions"]').trigger('click')
    expect(wrapper.find('[data-testid="bench-tab-versions"]').attributes('aria-selected')).toBe('true')
  })

  it('shows as many of the workshop’s eight tabs as the column holds', async () => {
    // The Chinese column fits five of eight: the owner's design case.
    stubLayout(NARROW)
    const wrapper = mountWorkshopBench()
    await flush()
    expect(tabTexts(wrapper)).toEqual(['概览', '上下文', '工作目录', '变更文件', '测试用例'])
    // The window has not moved yet, so there is nowhere to go back to.
    expect(wrapper.find('[data-testid="bench-tabs-prev"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="bench-tabs-next"]').attributes('disabled')).toBeUndefined()
  })

  it('shows fewer tabs when the labels are wider', async () => {
    // The English column cannot hold five of these; the count follows the
    // measurement rather than a number written into the component.
    stubLayout(WIDE)
    const wrapper = mountWorkshopBench()
    await flush()
    expect(tabTexts(wrapper)).toEqual(['概览', '上下文', '工作目录'])
    expect(wrapper.findAll('[role="tab"]').length).toBeLessThan(5)
    expect(wrapper.find('[data-testid="bench-tabs-next"]').attributes('disabled')).toBeUndefined()
  })

  it('moves the window with the arrows and leaves the selection alone', async () => {
    stubLayout(NARROW)
    const wrapper = mountWorkshopBench()
    await flush()
    for (let i = 0; i < 3; i += 1) {
      await wrapper.find('[data-testid="bench-tabs-next"]').trigger('click')
      await flush()
    }
    expect(tabTexts(wrapper)).toEqual(['变更文件', '测试用例', '评测', '触发描述', '版本'])
    // The end of the list disables the right arrow; the selection never moved.
    expect(wrapper.find('[data-testid="bench-tabs-next"]').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('[role="tab"]').every((tab) => tab.attributes('aria-selected') === 'false')).toBe(true)
    expect(wrapper.find('[data-testid="bench-tabs-prev"]').attributes('disabled')).toBeUndefined()
  })

  it('steps the window back one tab at a time', async () => {
    stubLayout(NARROW)
    const wrapper = mountWorkshopBench()
    await flush()
    for (let i = 0; i < 3; i += 1) {
      await wrapper.find('[data-testid="bench-tabs-next"]').trigger('click')
      await flush()
    }
    await wrapper.find('[data-testid="bench-tabs-prev"]').trigger('click')
    await flush()
    expect(tabTexts(wrapper)).toEqual(['工作目录', '变更文件', '测试用例', '评测', '触发描述'])
    expect(wrapper.find('[data-testid="bench-tabs-next"]').attributes('disabled')).toBeUndefined()
  })

  it('leaves the window where the arrows put it when the next one holds more', async () => {
    // One wide label leads the list: the first window holds four tabs and the
    // one after it holds five. The count changing under a moved window must not
    // drag the window back to the selection.
    stubLayout([120, ...Array<number>(7).fill(52)])
    const wrapper = mountWorkshopBench()
    await flush()
    expect(tabTexts(wrapper)).toEqual(['概览', '上下文', '工作目录', '变更文件'])
    await wrapper.find('[data-testid="bench-tabs-next"]').trigger('click')
    await flush()
    expect(tabTexts(wrapper)).toEqual(['上下文', '工作目录', '变更文件', '测试用例', '评测'])
  })

  it('brings a tab selected outside the window into view', async () => {
    stubLayout(NARROW)
    // The workshop opens on 变更文件 — the conversation page's tab remembered
    // for this scope — and the last tab here is a window away.
    localStorage.setItem('forebrain.workbench.tab.s:ws', 'versions')
    const wrapper = mountWorkshopBench()
    await flush()
    expect(tabTexts(wrapper)).toEqual(['变更文件', '测试用例', '评测', '触发描述', '版本'])
    expect(wrapper.find('[data-testid="bench-tab-versions"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('.bench-tabs').classes()).toContain('bench-tabs--moved')
  })

  it('pages the conversation page’s four tabs once they no longer fit', async () => {
    // Four English labels need more than the column has, so the same bar pages
    // them instead of drawing the last one over the arrow.
    stubLayout(WIDE)
    const wrapper = mountBench()
    await flush()
    expect(tabTexts(wrapper)).toEqual(['概览', '上下文', '工作目录'])
    expect(wrapper.find('[data-testid="bench-tabs-next"]').attributes('disabled')).toBeUndefined()
    await wrapper.find('[data-testid="bench-tabs-next"]').trigger('click')
    await flush()
    expect(tabTexts(wrapper)).toEqual(['上下文', '工作目录', '变更文件'])
    expect(wrapper.find('[data-testid="bench-tabs-next"]').attributes('disabled')).toBeDefined()
  })

  it('draws no arrows for the conversation page’s four tabs', async () => {
    stubLayout(NARROW)
    const wrapper = mountBench()
    await flush()
    expect(wrapper.find('[data-testid="bench-tabs-next"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="bench-tabs-prev"]').exists()).toBe(false)
    expect(wrapper.find('.bench-tabs').classes()).not.toContain('bench-tabs--paged')
  })

  it('is one width for the conversation page and the workshop', async () => {
    const wrapper = mountWorkshopBench()
    await flush()
    expect(wrapper.classes()).not.toContain('chat-workbench--wide')
  })

  // A control that is *about* one tab opens the panel on it, whatever this
  // conversation last read (prototype :4346) — the context ring lands on the
  // context even when 工作目录 was the tab last used. One initialiser decides, so
  // the two sources cannot overwrite each other in whatever order they run.
  it('opens on the tab a control asked for, over the tab last read', async () => {
    localStorage.setItem('forebrain.workbench.tab.s:1', 'tree')
    const wrapper = mount(ChatWorkbench, {
      props: {
        records: [], loading: false, error: '', overlay: false, scope: { sessionId: '1' },
        requestedTab: 'context',
      },
      global: { stubs: { teleport: true } },
    })
    await flush()
    expect(wrapper.find('[data-testid="bench-tab-context"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('[data-testid="bench-tab-tree"]').attributes('aria-selected')).toBe('false')
  })

  it('opens on the tab last read when nothing was asked for', async () => {
    localStorage.setItem('forebrain.workbench.tab.s:1', 'tree')
    const wrapper = mount(ChatWorkbench, {
      props: { records: [], loading: false, error: '', overlay: false, scope: { sessionId: '1' } },
      global: { stubs: { teleport: true } },
    })
    await flush()
    expect(wrapper.find('[data-testid="bench-tab-tree"]').attributes('aria-selected')).toBe('true')
  })

  // The request is a single-shot errand: the panel says the tab is on screen and
  // the page forgets it. This is the shape ChatView really has, so what the ring
  // produces on the real page is what is asserted here — including the second
  // half, that being forgotten does not send the panel back to the old tab.
  it('reports the requested tab as shown, and stays on it once the page forgets it', async () => {
    localStorage.setItem('forebrain.workbench.tab.s:1', 'tree')
    const Host = defineComponent({
      components: { ChatWorkbench },
      setup() {
        const requested = ref<string | null>('context')
        return { requested }
      },
      template: `<ChatWorkbench
        :records="[]" :loading="false" :error="''" :overlay="false"
        :scope="{ sessionId: '1' }"
        :requested-tab="requested"
        @requested-tab-shown="requested = null"
      />`,
    })
    const wrapper = mount(Host, { global: { stubs: { teleport: true } } })
    await flush()
    await flush()
    const child = wrapper.findComponent(ChatWorkbench)
    expect(child.emitted('requested-tab-shown')).toHaveLength(1)
    expect(wrapper.find('[data-testid="bench-tab-context"]').attributes('aria-selected')).toBe('true')
  })

  it('asks for the same tab again after the page forgot the first request', async () => {
    localStorage.setItem('forebrain.workbench.tab.s:1', 'tree')
    const Host = defineComponent({
      components: { ChatWorkbench },
      setup() {
        const requested = ref<string | null>('context')
        const ask = (tab: string) => { requested.value = tab }
        return { requested, ask }
      },
      template: `<div>
        <button type="button" data-testid="ring" @click="ask('context')">ring</button>
        <ChatWorkbench
          :records="[]" :loading="false" :error="''" :overlay="false"
          :scope="{ sessionId: '1' }"
          :requested-tab="requested"
          @requested-tab-shown="requested = null"
        />
      </div>`,
    })
    const wrapper = mount(Host, { global: { stubs: { teleport: true } } })
    await flush()
    // Read another tab by hand, then click the ring again: it is a new request,
    // not a value that was already sitting there.
    await wrapper.find('[data-testid="bench-tab-overview"]').trigger('click')
    await flush()
    expect(wrapper.find('[data-testid="bench-tab-overview"]').attributes('aria-selected')).toBe('true')

    await wrapper.find('[data-testid="ring"]').trigger('click')
    await flush()
    expect(wrapper.find('[data-testid="bench-tab-context"]').attributes('aria-selected')).toBe('true')
  })

  it('falls back to the tab last read when the request names a disabled tab', async () => {
    localStorage.setItem('forebrain.workbench.tab.s:1', 'tree')
    const wrapper = mount(ChatWorkbench, {
      props: {
        records: [], loading: false, error: '', overlay: false, scope: { sessionId: '1' },
        extraTabs: [{ key: 'cases', label: 'workshop.tabCases', disabled: true }],
        requestedTab: 'cases',
      },
      global: { stubs: { teleport: true } },
    })
    await flush()
    expect(wrapper.find('[data-testid="bench-tab-tree"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('[data-testid="bench-tab-cases"]').attributes('aria-selected')).toBe('false')
  })

  // The workbench shares the right-hand column with the subagent drawer, so it
  // collapses the way the drawer does: the same icon, the same word, the same
  // place (owner 2026-10-20). It used to be an × — the shape of closing a
  // dialog, which is not what happens to a column.
  it('collapses with the drawer’s icon and word, and still asks to close', async () => {
    const wrapper = mountBench()
    await flush()
    const button = wrapper.get('[data-testid="chat-workbench-close"]')
    expect(button.attributes('aria-label')).toBe('收起工作台')
    expect(button.get('svg').classes()).toContain('lucide-panel-right-close-icon')

    await button.trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  // The column's width is dragged by the edge it shares with the conversation
  // (owner 2026-10-21), and starts at the design's 340px.
  it('carries the handle on its left edge and the design width as its own', () => {
    const wrapper = mountBench()
    expect(wrapper.find('[data-testid="chat-workbench-resizer"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="chat-workbench-resizer"]').attributes('role')).toBe('separator')
    expect(wrapper.get('[data-testid="chat-workbench"]').attributes('style')).toContain('width: 340px')
  })

  // As an overlay the width is the layout's, not the handle's: an inline width
  // would beat the narrow-window rule that keeps the column on screen.
  it('writes no width of its own while it is an overlay', () => {
    const wrapper = mount(ChatWorkbench, {
      props: { records: [], loading: false, error: '', overlay: true, scope: { sessionId: 's1' } },
      global: { stubs: { teleport: true } },
    })
    expect(wrapper.get('[data-testid="chat-workbench"]').classes()).toContain('chat-workbench--overlay')
    expect(wrapper.get('[data-testid="chat-workbench"]').attributes('style')).toBeUndefined()
  })
})
