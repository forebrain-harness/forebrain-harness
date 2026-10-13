import { describe, expect, it, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'

import OverviewTab from './OverviewTab.vue'
import type { AgentRosterRow } from '@/lib/api'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

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
    inputTokens: 1000,
    outputTokens: 200,
    cacheRead: 0,
    cacheWritten: 0,
    uncached: 1000,
    requests: 2,
    cacheHitPercent: 50,
    toolCalls: 0,
    byTool: {},
  })
}

const running: AgentRosterRow = { id: 'a', kind: 'subagent', label: 'explore', status: 'running' } as AgentRosterRow

describe('OverviewTab', () => {
  afterEach(() => vi.restoreAllMocks())

  it('draws no running-agents card when nothing is running', async () => {
    stubApi()
    const wrapper = mount(OverviewTab, {
      props: { scope: { sessionId: 's1' }, records: [], loading: false, error: '' },
    })
    await flush()
    expect(wrapper.text()).not.toContain('运行中的代理')
    // The summary cards that always have something to say are there.
    expect(wrapper.text()).toContain('用量')
  })

  it('draws the running-agents card while an agent runs', async () => {
    stubApi()
    const wrapper = mount(OverviewTab, {
      props: { scope: { sessionId: 's1' }, records: [running], loading: false, error: '' },
    })
    await flush()
    expect(wrapper.text()).toContain('运行中的代理')
  })
})
