import { describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'

import SkillsTable from './SkillsTable.vue'
import tip from '@/directives/tip'
import type { SkillRecord } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

/**
 * The shared skill table (plan 073): the rows of one page, the boxes ticked for
 * a bulk download and the row's icon actions — the search box, the layer
 * filter and the buttons above it belong to the page that draws the table.
 */
function rows(): SkillRecord[] {
  return [
    { name: 'gateway-ops', description: 'runbook', origin: 'agent', enabled: true, editable: true, rootPath: '/s/gateway-ops', version: 3, state: 'published', downloadUrl: '/api/skills/gateway-ops/download' },
    { name: 'gateway-5xx', description: 'triage', origin: 'agent', enabled: false, editable: true, rootPath: '/s/.drafts/gateway-5xx', state: 'draft', draftOnly: true },
    { name: 'golang-testing', description: 'shared', origin: 'shared', enabled: true, rootPath: '/h/golang-testing', version: 2, update: { rev: 'd41c8e0' }, downloadUrl: '/api/skills/golang-testing/download' },
  ]
}

function mountTable(overrides: Record<string, unknown> = {}): VueWrapper {
  return mount(SkillsTable, {
    props: {
      rows: rows(),
      loading: false,
      saving: false,
      scope: 'agent',
      selected: new Set<string>(),
      ...overrides,
    },
    global: { directives: { tip } },
  })
}

describe('SkillsTable', () => {
  it('draws only the table — the search box and the bar above it belong to the page', () => {
    const wrapper = mountTable()

    expect(wrapper.find('input[type="search"]').exists()).toBe(false)
    expect(wrapper.find('.forebrain-list-toolbar').exists()).toBe(false)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-skill-row]')).toHaveLength(3)
  })

  it('shows version chips and badges; a draft row carries no toggle', () => {
    const wrapper = mountTable()
    // The published agent row shows its version and a switch.
    expect(wrapper.find('[data-skill-row="gateway-ops"]').text()).toContain('v3')
    expect(wrapper.find('[data-testid="skill-toggle-gateway-ops"]').exists()).toBe(true)
    // The draft row shows 草稿 and no switch.
    expect(wrapper.find('[data-skill-row="gateway-5xx"]').text()).toContain('草稿')
    expect(wrapper.find('[data-testid="skill-toggle-gateway-5xx"]').exists()).toBe(false)
    // The shared row is read-only here (no toggle) and flags its update.
    expect(wrapper.find('[data-testid="skill-toggle-golang-testing"]').exists()).toBe(false)
    expect(wrapper.find('[data-skill-row="golang-testing"]').text()).toContain('有更新')
  })

  it('names the layer only, and the description with the directory under it', () => {
    const wrapper = mountTable()

    const shared = wrapper.find('[data-skill-row="golang-testing"]')
    // 有更新 sits by the name, the layer badge in its own column, and the
    // layer's name is all it says — no primary agent name in front of it.
    expect(shared.findAll('.forebrain-badge').map((badge) => badge.text())).toEqual(['有更新', '共享', 'v2', '已启用'])
    expect(shared.text()).toContain('shared · /h/golang-testing')
    expect(shared.find('.forebrain-cell-1').text()).toBe('shared · /h/golang-testing')
  })

  it('offers 查看, 下载 zip and 删除 as icon buttons, and no text button at all', () => {
    const wrapper = mountTable()

    const own = wrapper.find('[data-skill-row="gateway-ops"]')
    expect(own.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual([
      '查看 gateway-ops',
      '下载 zip gateway-ops',
      '删除 gateway-ops',
    ])
    // Every action is one of the shared icon buttons; a text button would be
    // a `forebrain-btn` inside the action column.
    expect(own.findAll('td:last-child button.forebrain-btn')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('详情')
    // A row of another layer keeps the holes it has no action for.
    const inherited = wrapper.find('[data-skill-row="golang-testing"]')
    expect(inherited.findAll('.forebrain-icon-slot')).toHaveLength(1)
    expect(inherited.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual([
      '查看 golang-testing',
      '下载 zip golang-testing',
    ])
  })

  it('emits detail for an owned or updated row', async () => {
    const wrapper = mountTable()
    await wrapper.find('[data-testid="skill-detail-gateway-ops"]').trigger('click')
    expect(wrapper.emitted('detail')?.[0]?.[0]).toMatchObject({ name: 'gateway-ops' })
    // A shared row with an update also offers detail.
    expect(wrapper.find('[data-testid="skill-detail-golang-testing"]').exists()).toBe(true)
  })

  it('hands the ticked set back to the page, one box at a time or all of the page', async () => {
    const wrapper = mountTable()

    const boxes = wrapper.findAll('[data-skill-row="gateway-ops"] [role="checkbox"]')
    await boxes[0].trigger('click')
    expect(wrapper.emitted('update:selected')?.[0]?.[0]).toEqual(new Set(['gateway-ops']))

    // 全选 ticks the page on screen — a server-paged table has no other rows.
    await wrapper.find('[data-testid="skills-select-all"]').trigger('click')
    const all = wrapper.emitted('update:selected')?.[1]?.[0] as Set<string>
    expect([...all].sort()).toEqual(['gateway-5xx', 'gateway-ops', 'golang-testing'])
  })
})
