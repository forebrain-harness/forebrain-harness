import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import AgentRosterPanel from './AgentRosterPanel.vue'
import type { AgentRosterRow } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

/**
 * The roster is a control panel over the agents at work, so every control it
 * draws has to name an act the row it sits on can still do: stopping belongs to
 * a run in flight, and a row with no session has no conversation to open. The
 * terminal keeps the same rule (only a composer that takes input carries the
 * roster); the panel is this surface's drawing site for it.
 */
function row(overrides: Partial<AgentRosterRow> = {}): AgentRosterRow {
  return {
    id: 'task-1',
    kind: 'subagent',
    label: 'general-purpose',
    status: 'running',
    sessionId: 'session-1',
    title: 'network probe',
    ...overrides,
  } as AgentRosterRow
}

function mountPanel(records: AgentRosterRow[]) {
  return mount(AgentRosterPanel, {
    props: { title: 'Agents', records, loading: false, error: '' },
  })
}

describe('AgentRosterPanel', () => {
  it('offers stopping only on the rows that are running', async () => {
    const running = row()
    const finished = row({ id: 'task-2', sessionId: 'session-2', status: 'done', title: 'slow probe' })
    const wrapper = mountPanel([running, finished])

    const rows = wrapper.findAll('li')
    expect(rows).toHaveLength(2)
    const stopIn = (index: number) => rows[index].findAll('button').filter((button) => button.text() === '停止')
    expect(stopIn(0)).toHaveLength(1)
    expect(stopIn(1)).toHaveLength(0)

    await stopIn(0)[0].trigger('click')
    expect(wrapper.emitted('cancel')?.[0]?.[0]).toMatchObject({ id: 'task-1' })
  })

  it('draws no stop control at all once nothing is running', () => {
    const wrapper = mountPanel([
      row({ status: 'done' }),
      row({ id: 'task-2', sessionId: 'session-2', status: 'failed', title: 'slow probe' }),
    ])

    expect(wrapper.text()).not.toContain('全部停止')
    expect(wrapper.findAll('button').filter((button) => button.text() === '停止')).toHaveLength(0)
  })

  it('offers stop-all only while one of the rows runs', async () => {
    const wrapper = mountPanel([row(), row({ id: 'task-2', sessionId: 'session-2', status: 'done' })])

    const stopAll = wrapper.findAll('button').find((button) => button.text() === '全部停止')
    expect(stopAll).toBeTruthy()
    await stopAll?.trigger('click')
    expect(wrapper.emitted('cancel-all')).toBeTruthy()
  })

  it('opens a row only when it names a session', async () => {
    const wrapper = mountPanel([
      row(),
      row({ id: 'task-2', sessionId: '', status: 'done', title: 'slow probe' }),
    ])

    const views = wrapper.findAll('li').map((item) => item.find('button').element as HTMLButtonElement)
    expect(views[0].disabled).toBe(false)
    expect(views[1].disabled).toBe(true)

    await views[0].click()
    expect(wrapper.emitted('view')?.[0]?.[0]).toMatchObject({ id: 'task-1' })
    expect(wrapper.emitted('view')).toHaveLength(1)
  })
})
