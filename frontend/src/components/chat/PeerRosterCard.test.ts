import { beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import PeerRosterCard from './PeerRosterCard.vue'
import type { ReadableAnswer } from '@/composables/useChatStream'

/**
 * The roster a peer_list answer draws: identity blocks, the engine's names,
 * the status in the reader's words, what each agent is doing, and where it
 * works. Two subagents of one type are told apart by the short key the engine
 * derived — the card never shows the same name twice.
 */
describe('PeerRosterCard', () => {
  beforeAll(() => setLocale('zh'))

  const roster = (peers: ReadableAnswer['peers']): ReadableAnswer => ({ rows: [], peers })

  it('draws one row per agent, in the answer order', () => {
    const wrapper = mount(PeerRosterCard, {
      props: {
        answer: roster([
          { name: 'subagent-da6bdfef', kind: 'explore', status: 'running', task: 'Extract UI semantics', display: 'explore', short: 'da6b', own: true, mark: 'da', tone: 1 },
          { name: 'subagent-7fe93f3a', kind: 'explore', status: 'finished', task: 'Extract structure', display: 'explore', short: '7fe9', own: true, mark: '7f', tone: 1 },
          { name: 'proj-99', kind: 'terminal', status: 'idle', cwd: '/work/proj', sameTree: true, activeAgo: '20m', display: 'proj-99', mark: '99', tone: 2 },
          { name: 'proj-7', kind: 'gateway conversation', status: 'busy', task: 'fix login', display: 'proj-7', mark: '07', tone: 3 },
        ]),
      },
    })

    const rows = wrapper.findAll('[data-testid="peer-roster-row"]')
    expect(rows).toHaveLength(4)
    expect(rows.map((row) => row.find('.nm').text())).toEqual([
      'explore · da6b',
      'explore · 7fe9',
      'proj-99',
      'proj-7',
    ])
    expect(rows.map((row) => row.find('.st').text())).toEqual(['进行中', '已完成', '空闲', '工作中'])
    // What each agent is doing, and where it works.
    expect(rows[0].find('.l2').text()).toBe('Extract UI semantics')
    expect(rows[2].find('.l3').text()).toBe('终端 · 同目录 · 20 分钟前')
    expect(rows[2].find('.l3').classes()).not.toContain('path')
    expect(rows[3].find('.l3').text()).toBe('网页会话')
    // The identity block: outlined for this conversation's subagents, filled
    // for an agent of another process.
    const marks = wrapper.findAll('[data-testid="peer-mark"]')
    expect(marks).toHaveLength(4)
    expect(marks[0].classes()).toContain('peer-mark--sub')
    expect(marks[2].classes()).toContain('peer-mark--agent')
    expect(marks[2].text()).toBe('99')
  })

  it('elides a directory from its head and words the idle time', () => {
    const wrapper = mount(PeerRosterCard, {
      props: {
        answer: roster([
          { name: 'proj-5', kind: 'terminal', status: 'idle', cwd: '/private/var/folders/kw/T/fb-proto-run/proj', activeAgo: '2h', display: 'proj-5', mark: '05', tone: 0 },
        ]),
      },
    })
    const place = wrapper.find('.l3')
    expect(place.classes()).toContain('path')
    expect(place.text()).toBe('终端 · /private/var/folders/kw/T/fb-proto-run/proj · 2 小时前')
  })

  it('says a roster is empty instead of drawing nothing', () => {
    const wrapper = mount(PeerRosterCard, {
      props: { answer: { rows: [], peers: [], note: 'No other agents are reachable right now.' } },
    })
    expect(wrapper.find('[data-testid="peer-roster-empty"]').text()).toBe('没有可以联系的代理。')
    expect(wrapper.findAll('[data-testid="peer-roster-row"]')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('No other agents')
  })
})
