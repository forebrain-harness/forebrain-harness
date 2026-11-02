import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import { mountSettingsTab } from './form/settingsTestHost'
import SettingsWebToolsTab from './SettingsWebToolsTab.vue'

/** The web-tools tab (plan 191 §0.15, §4.3): web search with its provider and
 * keys, and web fetch with its own. A key is known only as "set + its last
 * four characters" (§0.20): 更换 opens a password box, a blank one keeps what
 * is stored, and 取消 goes back to the mask. */
describe('SettingsWebToolsTab', () => {
  const TAVILY_KEY = 'agents.defaults.web_search.tavily.api_key'

  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({
      values: {
        'agents.defaults.web_search.provider': 'tavily',
        'agents.defaults.web_search.tavily.search_depth': 'basic',
        'agents.defaults.web_fetch.provider': '',
        'agents.defaults.web_fetch.allow_private_ip': false,
      },
      secrets: { [TAVILY_KEY]: { set: true, last4: 'Y1s8' } },
      // claudeCodeAvailable: false — a machine without Claude Code, so the
      // peers group stays hidden (plan 195) and this tab keeps its rows.
      meta: { approvalPresetsVisible: true, claudeCodeAvailable: false, builtinHandoffPrompt: 'builtin prompt' },
    })
    vi.spyOn(forebrainApi, 'saveSettings').mockResolvedValue({ saved: true, applies: 'next_turn' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  async function mountTab() {
    const wrapper = await mountSettingsTab({ tab: 'net', component: SettingsWebToolsTab })
    await flushPromises()
    return wrapper
  }

  it('draws the two groups with the provider choices in the prototype\'s words', async () => {
    const wrapper = await mountTab()
    expect(wrapper.findAll('.settings-group h3').map((title) => title.text())).toEqual(['联网搜索', '网页抓取'])

    const search = wrapper.find('[data-group="ws"]')
    const provider = search.find('[data-field="agents.defaults.web_search.provider"]')
    const options = provider.findAll('[role="radio"]')
    expect(provider.findAll('.settings-radio').map((row) => row.text())).toEqual(['不启用', 'Tavily', 'Brave', '百度', 'DeepSeek'])
    expect(options.map((option) => option.attributes('aria-checked'))).toEqual(['false', 'true', 'false', 'false', 'false'])

    const fetch = wrapper.find('[data-group="wf"]')
    const fetchProvider = fetch.find('[data-field="agents.defaults.web_fetch.provider"]')
    expect(fetchProvider.findAll('.settings-radio').map((row) => row.text())).toEqual(['内置', 'DeepSeek'])
    expect(fetchProvider.findAll('[role="radio"]')[0].attributes('aria-checked')).toBe('true')
  })

  it('shows only the chosen provider\'s rows', async () => {
    const wrapper = await mountTab()
    const search = wrapper.find('[data-group="ws"]')
    expect(search.find(`[data-field="${TAVILY_KEY}"]`).exists()).toBe(true)
    expect(search.find('[data-field="agents.defaults.web_search.tavily.search_depth"]').exists()).toBe(true)
    expect(search.find('[data-field="agents.defaults.web_search.brave.api_key"]').exists()).toBe(false)

    await search.findAll('[data-field="agents.defaults.web_search.provider"] [role="radio"]')[2].trigger('click')
    expect(search.find(`[data-field="${TAVILY_KEY}"]`).exists()).toBe(false)
    expect(search.find('[data-field="agents.defaults.web_search.brave.api_key"]').exists()).toBe(true)

    const fetch = wrapper.find('[data-group="wf"]')
    expect(fetch.find('[data-field="agents.defaults.web_fetch.deepseek.api_key"]').exists()).toBe(false)
    await fetch.findAll('[data-field="agents.defaults.web_fetch.provider"] [role="radio"]')[1].trigger('click')
    expect(fetch.find('[data-field="agents.defaults.web_fetch.deepseek.api_key"]').exists()).toBe(true)
  })

  it('masks a stored key to its last four, and 更换/取消 walks the box back to the mask', async () => {
    const wrapper = await mountTab()
    const row = wrapper.find(`[data-field="${TAVILY_KEY}"]`)
    expect(row.find('[data-masked]').text()).toBe('••••••••Y1s8')

    await row.find('[data-secret-edit]').trigger('click')
    const box = row.find(`[data-secret="${TAVILY_KEY}"]`)
    expect((box.element as HTMLInputElement).type).toBe('password')

    await row.find('[data-secret-cancel]').trigger('click')
    expect(wrapper.find(`[data-field="${TAVILY_KEY}"]`).find('[data-masked]').text()).toBe('••••••••Y1s8')
  })

  it('saves a typed key as a secret, and a blank box keeps the stored one', async () => {
    const wrapper = await mountTab()
    const group = wrapper.find('[data-group="ws"]')
    await group.find('[data-secret-edit]').trigger('click')
    await group.find(`[data-secret="${TAVILY_KEY}"]`).setValue('tvly-new-9876')

    // A second row changes too, so the blank-box case below still has a save.
    await group.find('[data-field="agents.defaults.web_search.tavily.search_depth"]').findAll('[role="radio"]')[1].trigger('click')
    await group.find('[data-save="ws"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.saveSettings).toHaveBeenCalledWith(
      { 'agents.defaults.web_search.tavily.search_depth': 'advanced' },
      { [TAVILY_KEY]: 'tvly-new-9876' },
    )
    // The value never comes back; what the page now knows is that one is set.
    expect(group.find('[data-masked]').text()).toBe('••••••••9876')

    // 更换 left open with nothing typed: the next save carries values only —
    // a blank box never travels, so the stored key is kept as it is.
    await group.find('[data-secret-edit]').trigger('click')
    await group
      .find('[data-field="agents.defaults.web_search.tavily.search_depth"]')
      .findAll('[role="radio"]')[0]
      .trigger('click')
    await group.find('[data-save="ws"]').trigger('click')
    await flushPromises()
    const calls = vi.mocked(forebrainApi.saveSettings).mock.calls
    expect(calls[calls.length - 1]?.[0]).toEqual({ 'agents.defaults.web_search.tavily.search_depth': 'basic' })
    expect(calls[calls.length - 1]?.[1]).toEqual({})
  })

  it('draws 允许内网地址 as the warning switch the prototype draws', async () => {
    const wrapper = await mountTab()
    const row = wrapper.find('[data-field="agents.defaults.web_fetch.allow_private_ip"]')
    expect(row.find('b').text()).toBe('允许内网地址')
    expect(row.find('[data-switch="agents.defaults.web_fetch.allow_private_ip"]').attributes('aria-checked')).toBe(
      'false',
    )
  })
})
