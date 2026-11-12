import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type Stubs, type VueWrapper } from '@vue/test-utils'
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

async function mountView(query = '?tab=appearance', stubs?: Stubs): Promise<VueWrapper> {
  setLocale('en')
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  await router.push('/settings' + query)
  const wrapper = mount(SettingsView, { global: { plugins: [router], ...(stubs ? { stubs } : {}) } })
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
      'Log',
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

  // The log page lives in the advanced group (plan 288) and takes the whole
  // window: the design's `.set.wide` instead of the reading column.
  it('opens the log page in the advanced group, at the window’s own width', async () => {
    mockSettings(true)
    const wrapper = await mountView('?tab=logs', {
      LogsTab: { props: ['session', 'from', 'to', 'focus'], template: '<div data-testid="logs-tab-stub" />' },
    })

    const advanced = wrapper.find('.settings-group[data-group="advanced"]')
    expect(advanced.text()).toBe('Advanced')
    const selected = wrapper.findAll('[role="tab"]').filter((tab) => tab.attributes('aria-selected') === 'true')
    expect(selected.map((tab) => tab.text())).toEqual(['Log'])
    expect(wrapper.find('.settings-layout').classes()).toContain('settings-layout--wide')
    expect(wrapper.find('[data-testid="logs-tab-stub"]').exists()).toBe(true)
  })

  // A link from a failed turn's card names the conversation and the turn's
  // window; the page hands them to the log tab as its preselected filters.
  it('hands the URL’s conversation and window to the log tab', async () => {
    mockSettings(true)
    const wrapper = await mountView(
      '?tab=logs&session=s9&from=2026-11-11T02:00:00Z&to=2026-11-11T03:00:00Z&focus=r1',
      {
        LogsTab: {
          props: ['session', 'from', 'to', 'focus'],
          template: '<div data-testid="logs-tab-stub" />',
        },
      },
    )

    const stub = wrapper.findComponent('[data-testid="logs-tab-stub"]') as unknown as VueWrapper
    expect(stub.props()).toMatchObject({
      session: 's9',
      from: '2026-11-11T02:00:00Z',
      to: '2026-11-11T03:00:00Z',
      focus: 'r1',
    })
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
