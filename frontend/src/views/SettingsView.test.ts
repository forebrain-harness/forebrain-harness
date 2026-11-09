import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

import SettingsView from './SettingsView.vue'

/**
 * The settings page's sub-navigation (plan 191 §4.2): three groups of tabs in
 * the prototype's order over `?tab=`, with the approval tab present only where
 * a sandbox exists to configure (plan 215 §3.4). Every tab's own content is its
 * component's business.
 */
function mockSettings(approvalPresetsVisible: boolean) {
  vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
    values: {},
    secrets: {},
    // claudeCodeAvailable: false — no Claude Code on the mocked machine, so the
    // peers group stays hidden (plan 195) and the tab lists below stay as they were.
    meta: { approvalPresetsVisible, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin' },
  })
}

async function mountView(query = '?tab=appearance'): Promise<VueWrapper> {
  setLocale('en')
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  await router.push('/settings' + query)
  const wrapper = mount(SettingsView, { global: { plugins: [router] } })
  await router.isReady()
  await flushPromises()
  return wrapper
}

describe('SettingsView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('groups the tabs under the three headings, in the design order', async () => {
    mockSettings(true)
    const wrapper = await mountView()
    const groups = wrapper.findAll('.settings-group')
    expect(groups.map((group) => group.text())).toEqual(['General', 'Global services', 'Advanced'])
    expect(groups.map((group) => group.attributes('data-group'))).toEqual(['general', 'services', 'advanced'])
  })

  it('lists the prototype tabs, 会话 and 联网工具 included', async () => {
    mockSettings(true)
    const wrapper = await mountView()
    const nav = wrapper.find('[data-testid="settings-tabs"]')
    const order = nav.findAll('[role="tab"]').map((tab) => tab.text())
    expect(order).toEqual([
      'Appearance',
      'Session',
      'Approval & sandbox',
      'Subagents',
      'Web tools',
      'Models',
      'MCP',
      'Language servers',
      'Hooks',
      'Scheduled tasks',
      'Memory',
      'Shared skills',
      'Variables',
      'Config file',
      'Runtime status',
    ])
    // A heading never becomes a tab, and never takes `?tab=`.
    expect(nav.findAll('.settings-group').every((group) => group.attributes('role') === undefined)).toBe(true)
  })

  it('opens the tab the URL names and marks it selected', async () => {
    mockSettings(true)
    const wrapper = await mountView('?tab=net')
    const selected = wrapper.findAll('[role="tab"]').filter((tab) => tab.attributes('aria-selected') === 'true')
    expect(selected.map((tab) => tab.text())).toEqual(['Web tools'])
    expect(wrapper.find('[data-tab="net"]').exists()).toBe(true)
  })

  // A gateway with no sandbox has one posture, so the tab that configures the
  // approval presets is not offered at all, and a link straight to it lands on
  // the appearance tab instead of on nothing (owner 2026-10-31, plan 215 §3.4).
  it('hides the approval tab when the machine has no sandbox', async () => {
    mockSettings(false)
    const wrapper = await mountView('?tab=approval')
    const tabs = wrapper.findAll('[role="tab"]').map((tab) => tab.text())
    expect(tabs).not.toContain('Approval and sandbox')
    const selected = wrapper.findAll('[role="tab"]').filter((tab) => tab.attributes('aria-selected') === 'true')
    expect(selected.map((tab) => tab.text())).toEqual(['Appearance'])
    expect(wrapper.text()).not.toContain('YOLO')
  })
})
