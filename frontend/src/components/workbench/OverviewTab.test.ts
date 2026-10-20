import { describe, expect, it, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'

import OverviewTab from './OverviewTab.vue'
import type { AgentRosterRow, ActionRecord } from '@/lib/api'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

function stubApi(overrides: { plans?: unknown[]; asks?: ActionRecord[] } = {}) {
  vi.spyOn(forebrainApi, 'sessionTodos').mockResolvedValue({ items: [] })
  vi.spyOn(forebrainApi, 'sessionPlans').mockResolvedValue((overrides.plans ?? []) as never)
  vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue(overrides.asks ?? [])
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

/** One answered round as the actions table keeps it. */
function answeredAsk(id: string, prompt: string, answeredAt: number, other = ''): ActionRecord {
  return {
    id,
    kind: 'user_interaction',
    status: 'answered',
    payloadJson: JSON.stringify({
      questions: [{ id: '范围', prompt, options: [{ id: 'web', label: '只改网页' }, { id: 'engine', label: '引擎也改' }] }],
    }),
    answerJson: JSON.stringify({
      answers: [{ question_id: '范围', option_ids: ['engine'], other_text: other }],
    }),
    createdAt: answeredAt - 10,
    updatedAt: answeredAt,
  }
}

function plan(name: string, markdown: string, updatedAt: number) {
  return { name, path: `plans/p/s1/${name}`, updatedAt, markdown }
}

const running: AgentRosterRow = { id: 'a', kind: 'subagent', label: 'explore', status: 'running' } as AgentRosterRow

function mountTab() {
  return mount(OverviewTab, {
    props: { scope: { sessionId: 's1' }, records: [], loading: false, error: '' },
  })
}

describe('OverviewTab', () => {
  afterEach(() => vi.restoreAllMocks())

  it('draws no running-agents card when nothing is running', async () => {
    stubApi()
    const wrapper = mountTab()
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

  it('draws no answers or plans card for a conversation that never asked or planned', async () => {
    stubApi()
    const wrapper = mountTab()
    await flush()
    expect(wrapper.find('[data-testid="bench-answers"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="bench-plans"]').exists()).toBe(false)
  })

  // A long conversation asks several rounds. The card keeps them, newest
  // first, and expanding one closes the other: two rounds open at once would
  // push the cards below off the column.
  it('lists every answered round and keeps exactly one expanded', async () => {
    stubApi({
      asks: [answeredAsk('a-old', '这次改到哪一层？', 200), answeredAsk('a-new', '下一步做什么？', 900)],
    })
    const wrapper = mountTab()
    await flush()

    const card = wrapper.get('[data-testid="bench-answers"]')
    expect(card.text()).toContain('问答')
    expect(card.text()).toContain('2 次')
    expect(card.text()).toContain('1 个问题')
    // Newest first.
    expect(wrapper.findAll('[data-testid^="bench-ask-a-"]').map((row) => row.attributes('data-testid')))
      .toEqual(['bench-ask-a-new', 'bench-ask-a-old'])
    // The newest round starts open; the other is a single line.
    expect(wrapper.findAll('[data-testid="bench-ask-body"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="bench-ask-a-new"]').attributes('aria-expanded')).toBe('true')

    // Opening the older round closes the newer one.
    await wrapper.get('[data-testid="bench-ask-a-old"]').trigger('click')
    const bodies = wrapper.findAll('[data-testid="bench-ask-body"]')
    expect(bodies).toHaveLength(1)
    expect(bodies[0].text()).toContain('这次改到哪一层？')
    expect(wrapper.get('[data-testid="bench-ask-a-new"]').attributes('aria-expanded')).toBe('false')
  })

  it('shows each answer under its question, straight from the stored answer', async () => {
    stubApi({ asks: [answeredAsk('a1', '这次改到哪一层？', 200, '顺手补测试')] })
    const wrapper = mountTab()
    await flush()

    const body = wrapper.get('[data-testid="bench-ask-body"]')
    expect(body.text()).toContain('这次改到哪一层？')
    // The picked option is drawn by its label, and a typed answer beside it.
    expect(body.text()).toContain('→ 引擎也改')
    expect(body.text()).toContain('→ 顺手补测试')
  })

  // Every plan the conversation wrote is a row, newest first; the row carries
  // the file's own checklist progress rather than any claim beside it.
  it('lists every plan by updatedAt, newest first, with its checklist progress', async () => {
    stubApi({
      plans: [
        plan('older.md', '# Plan: 旧的一版\n\n- [x] 一\n- [ ] 二\n- [ ] 三', 300),
        plan('newest.md', '# Plan: 最新一版\n\n- [x] 一\n- [x] 二\n- [ ] 三', 900),
        plan('middle.md', '# Plan: 中间一版\n\n- [ ] 一\n- [ ] 二', 600),
      ],
    })
    const wrapper = mountTab()
    await flush()

    const card = wrapper.get('[data-testid="bench-plans"]')
    expect(card.text()).toContain('计划')
    expect(card.text()).toContain('3 份')
    const rows = wrapper.findAll('[data-testid^="bench-plan-"]').filter((row) => row.attributes('aria-expanded') !== undefined)
    expect(rows.map((row) => row.attributes('data-testid')))
      .toEqual(['bench-plan-newest.md', 'bench-plan-middle.md', 'bench-plan-older.md'])
    expect(wrapper.get('[data-testid="bench-plan-newest.md"]').text()).toContain('2/3')
    expect(wrapper.get('[data-testid="bench-plan-middle.md"]').text()).toContain('0/2')
    expect(wrapper.get('[data-testid="bench-plan-older.md"]').text()).toContain('1/3')
    // The checklist icon, not a word, says the number is progress.
    expect(wrapper.get('[data-testid="bench-plan-newest.md"] svg').element.tagName).toBe('svg')
  })

  it('asks the page to open the plan instead of showing it in a dialog', async () => {
    stubApi({ plans: [plan('only.md', '# Plan: 只有一份\n\n- [x] 一', 300)] })
    const wrapper = mountTab()
    await flush()

    await wrapper.get('[data-testid="bench-plan-body"] .bench-link').trigger('click')

    const opened = wrapper.emitted('open-plan')
    expect(opened).toHaveLength(1)
    expect((opened![0][0] as { name: string }).name).toBe('only.md')
    // The plan's text is not printed here at all: the card carries the row and
    // its progress, and the document belongs to the drawer's plan tab.
    expect(wrapper.find('pre').exists()).toBe(false)
  })
})
