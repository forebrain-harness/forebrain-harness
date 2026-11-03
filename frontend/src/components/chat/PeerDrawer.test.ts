import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import type { PeerAgentRow, PeerFileRow, PeerHeldRow } from '@/lib/api'
import PeerDrawer from './PeerDrawer.vue'

const NOW = Date.UTC(2026, 9, 30, 12, 0, 0)

function agent(overrides: Partial<PeerAgentRow> = {}): PeerAgentRow {
  return {
    name: 'forebrain-harness-a3',
    kind: 'terminal',
    status: 'busy',
    task: '给存活判定补 pidDomain 分支',
    cwd: '/repo',
    mark: 'a3',
    tone: 0,
    self: false,
    ...overrides,
  }
}

function file(overrides: Partial<PeerFileRow> = {}): PeerFileRow {
  return {
    dir: 'pkg/process',
    name: 'open.go',
    agents: [{ name: 'forebrain-harness-28', mark: '28', tone: 1, self: false }],
    last_ms: NOW - 6 * 60_000,
    clash: false,
    ...overrides,
  }
}

function held(overrides: Partial<PeerHeldRow> = {}): PeerHeldRow {
  return {
    msg_id: 'm1',
    from_name: 'forebrain-harness-28',
    preview: 'are you still editing open.go?',
    cause: 'mode-mismatch',
    held_at_ms: NOW,
    mark: '28',
    tone: 1,
    ...overrides,
  }
}

/** The drawer's own defaults, so each case states only what it is about. */
function mountDrawer(props: Partial<{
  held: PeerHeldRow[]
  files: PeerFileRow[]
  sameTree: PeerAgentRow[]
  other: PeerAgentRow[]
  otherTotal: number
  page: number
  pageSize: number
  now: number
  overlay: boolean
}> = {}) {
  return mount(PeerDrawer, {
    props: {
      held: [],
      files: [],
      sameTree: [],
      other: [],
      otherTotal: 0,
      page: 1,
      pageSize: 20,
      now: NOW,
      overlay: false,
      ...props,
    },
  })
}

describe('PeerDrawer', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))

  it('lists the held messages first, then the files, the directory and the rest', () => {
    const wrapper = mountDrawer({
        held: [held()],
        files: [file()],
        sameTree: [agent()],
        other: [agent({ name: 'forebrain-gw-3c1a', mark: '3c', tone: 2, kind: 'gateway conversation', status: 'idle', task: '' })],
        otherTotal: 1,
        page: 1,
        pageSize: 20,
        now: NOW,
    })
    const sections = wrapper.findAll('.peer-sec')
    expect(sections).toHaveLength(4)
    expect(sections[0]!.text()).toContain('Waiting for you')
    expect(sections[0]!.text()).toContain('forebrain-harness-28')
    expect(sections[1]!.text()).toContain('Files being edited in this directory')
    expect(sections[1]!.text()).toContain('pkg/process/')
    expect(sections[1]!.text()).toContain('open.go')
    expect(sections[2]!.text()).toContain('This directory')
    expect(sections[3]!.text()).toContain('Other directories')
    expect(sections[3]!.text()).toContain('/repo')
    wrapper.unmount()
  })

  it('marks a file both agents are editing, and says so in place of the time', () => {
    const wrapper = mountDrawer({
        files: [file({
          clash: true,
          agents: [
            { name: 'me', mark: 'me', tone: 0, self: true },
            { name: 'forebrain-harness-28', mark: '28', tone: 1, self: false },
          ],
        })],
        now: NOW,
    })
    const row = wrapper.find('[data-testid="peer-file-open.go"]')
    expect(row.classes()).toContain('clash')
    expect(row.text()).toContain('Both editing')
    expect(row.text()).not.toContain('min ago')
    wrapper.unmount()
  })

  it('pages the other directories and reports the range', async () => {
    const wrapper = mountDrawer({
        other: [agent({ name: 'forebrain-gw-3c1a', mark: '3c', tone: 2 })],
        otherTotal: 3,
        page: 1,
        pageSize: 1,
        now: NOW,
    })
    expect(wrapper.find('.pager').text()).toContain('1–1 of 3')
    expect(wrapper.find('[data-testid="peer-pager-prev"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="peer-pager-next"]').attributes('disabled')).toBeUndefined()

    await wrapper.find('[data-testid="peer-pager-next"]').trigger('click')
    expect(wrapper.emitted('page-change')?.[0]).toEqual([2])
    wrapper.unmount()
  })

  it('reports the two answers a held message can be given', async () => {
    const wrapper = mountDrawer({ held: [held()] })
    await wrapper.find('[data-testid="peer-drawer-release-m1"]').trigger('click')
    expect(wrapper.emitted('release')?.[0]).toEqual([held()])
    await wrapper.find('[data-testid="peer-drawer-deny-m1"]').trigger('click')
    expect(wrapper.emitted('deny')?.[0]).toEqual([held()])
    wrapper.unmount()
  })

  it('says when nothing is waiting and no file is being edited', () => {
    const wrapper = mountDrawer()
    // With nothing waiting the first section is not drawn at all.
    expect(wrapper.findAll('.peer-sec')).toHaveLength(3)
    expect(wrapper.text()).toContain('No files are being edited in this directory')
    expect(wrapper.text()).toContain('No agents in other directories')
    wrapper.unmount()
  })

  it('names an idle agent in the directory rather than leaving the row blank', () => {
    const wrapper = mountDrawer({ sameTree: [agent({ status: 'idle', task: '' })] })
    expect(wrapper.text()).toContain('Idle')
    wrapper.unmount()
  })
})
