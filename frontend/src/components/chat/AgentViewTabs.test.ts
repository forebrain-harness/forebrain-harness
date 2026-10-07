import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import AgentViewTabs from './AgentViewTabs.vue'
import type { SubagentTranscript } from '@/composables/useChatStream'

function entry(overrides: Partial<SubagentTranscript> = {}): SubagentTranscript {
  return {
    agentId: 'subagent-8c5c1a2b',
    agentType: 'general-purpose',
    title: '任务16 migrate 导入',
    task: 'run the whole migration and report the row counts you touched',
    status: 'running',
    blocks: [],
    inputTokens: 0,
    outputTokens: 0,
    updatedSeq: 1,
    ...overrides,
  }
}

describe('AgentViewTabs', () => {
  beforeAll(() => setLocale('en'))

  it('names a tab by its type and its task title, the way the roster row is named', () => {
    const wrapper = mount(AgentViewTabs, {
      props: { records: [entry()], active: '', seen: {} },
    })
    const tab = wrapper.findAll('button')[1] // 0 is the primary view
    expect(tab.text()).toContain('general-purpose · 任务16 migrate 导入')
  })

  it('hovers the full title, never the dispatch prompt', () => {
    const wrapper = mount(AgentViewTabs, {
      props: { records: [entry()], active: '', seen: {} },
    })
    const tab = wrapper.findAll('button')[1]
    expect(tab.attributes('title')).toBe('任务16 migrate 导入')
    expect(tab.attributes('title')).not.toContain('row counts')
  })

  it('shows the type alone for a record with no title, still not the prompt', () => {
    const wrapper = mount(AgentViewTabs, {
      props: { records: [entry({ title: undefined })], active: '', seen: {} },
    })
    const tab = wrapper.findAll('button')[1]
    expect(tab.attributes('title')).toBe('subagent-8c5c1a2b')
    expect(tab.text()).toContain('general-purpose')
    expect(tab.text()).not.toContain('run the whole migration')
  })
})
