import { describe, expect, it } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import { setLocale } from '@/locales'

import SettingsView from './SettingsView.vue'

/**
 * The settings page's sub-navigation (plan 076): three groups of tabs, in the
 * design's order, over `?tab=`. Every tab's own content is its component's
 * business.
 */
async function mountView(): Promise<VueWrapper> {
  setLocale('en')
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  await router.push('/settings?tab=appearance')
  const wrapper = mount(SettingsView, { global: { plugins: [router] } })
  await router.isReady()
  return wrapper
}

describe('SettingsView', () => {
  it('groups the tabs under the three headings, in the design\'s order', async () => {
    const wrapper = await mountView()
    const groups = wrapper.findAll('.settings-group')
    expect(groups.map((group) => group.text())).toEqual(['General', 'Global services', 'Advanced'])
    expect(groups.map((group) => group.attributes('data-group'))).toEqual(['general', 'services', 'advanced'])
  })

  it('lists every tab under its own heading, with the tab keys unchanged', async () => {
    const wrapper = await mountView()
    const nav = wrapper.find('[data-testid="settings-tabs"]')
    const order = nav.findAll('[role="tab"]').map((tab) => tab.text())
    expect(order).toEqual([
      'Appearance',
      'Approval default',
      'MCP',
      'Language servers',
      'Hooks',
      'Scheduled tasks',
      'Memory switches',
      'Shared skills',
      'Config file',
      'Runtime status',
    ])
    // A heading never becomes a tab, and never takes `?tab=`.
    expect(nav.findAll('.settings-group').every((group) => group.attributes('role') === undefined)).toBe(true)
  })

  it('opens the tab the URL names and marks it selected', async () => {
    const wrapper = await mountView()
    const selected = wrapper.findAll('[role="tab"]').filter((tab) => tab.attributes('aria-selected') === 'true')
    expect(selected.map((tab) => tab.text())).toEqual(['Appearance'])
  })
})
