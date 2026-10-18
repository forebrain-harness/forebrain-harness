import { beforeAll, describe, expect, it } from 'vitest'
import { config, mount } from '@vue/test-utils'

import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import AgentViewTabs from './AgentViewTabs.vue'
import type { SubagentTranscript } from '@/composables/useChatStream'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

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

  it('keeps the full title for the tip on the cut-off label, never the prompt', () => {
    const wrapper = mount(AgentViewTabs, {
      props: { records: [entry()], active: '', seen: {} },
    })
    const tab = wrapper.find('[data-testid="agent-tab-subagent-8c5c1a2b"]')
    // No native title: the tab names itself with the app's tip when its label
    // is cut off, and the dispatch prompt is never what it shows.
    expect(tab.attributes('title')).toBeUndefined()
    expect(tab.find('.truncate').attributes('title')).toBeUndefined()
    expect(tab.text()).not.toContain('row counts')
  })

  it('shows the type alone for a record with no title, still not the prompt', () => {
    const wrapper = mount(AgentViewTabs, {
      props: { records: [entry({ title: undefined })], active: '', seen: {} },
    })
    const tab = wrapper.find('[data-testid="agent-tab-subagent-8c5c1a2b"]')
    expect(tab.attributes('title')).toBeUndefined()
    expect(tab.text()).toContain('general-purpose')
    expect(tab.text()).not.toContain('run the whole migration')
  })
})
