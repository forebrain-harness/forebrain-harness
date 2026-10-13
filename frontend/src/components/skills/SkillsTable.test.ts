import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import SkillsTable from './SkillsTable.vue'
import type { SkillRecord } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

function rows(): SkillRecord[] {
  return [
    { name: 'gateway-ops', description: 'runbook', origin: 'agent', enabled: true, editable: true, rootPath: '/s/gateway-ops', version: 3, state: 'published', downloadUrl: '/api/skills/gateway-ops/download' },
    { name: 'gateway-5xx', description: 'triage', origin: 'agent', enabled: false, editable: true, rootPath: '/s/.drafts/gateway-5xx', state: 'draft', draftOnly: true },
    { name: 'golang-testing', description: 'shared', origin: 'shared', enabled: true, rootPath: '/h/golang-testing', version: 2, update: { rev: 'd41c8e0' } },
  ]
}

describe('SkillsTable', () => {
  it('shows version chips and badges; a draft row carries no toggle', () => {
    const wrapper = mount(SkillsTable, {
      props: { rows: rows(), loading: false, saving: false, downloading: false, ownerOrigin: 'agent' },
    })
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

  it('emits detail for an owned or updated row', async () => {
    const wrapper = mount(SkillsTable, {
      props: { rows: rows(), loading: false, saving: false, downloading: false, ownerOrigin: 'agent' },
    })
    await wrapper.find('[data-testid="skill-detail-gateway-ops"]').trigger('click')
    expect(wrapper.emitted('detail')?.[0]?.[0]).toMatchObject({ name: 'gateway-ops' })
    // A shared row with an update also offers detail.
    expect(wrapper.find('[data-testid="skill-detail-golang-testing"]').exists()).toBe(true)
  })

  it('filters by search text', async () => {
    const wrapper = mount(SkillsTable, {
      props: { rows: rows(), loading: false, saving: false, downloading: false, ownerOrigin: 'agent' },
    })
    await wrapper.find('[data-testid="skills-search"]').setValue('gateway')
    expect(wrapper.findAll('[data-skill-row]')).toHaveLength(2)
  })
})
