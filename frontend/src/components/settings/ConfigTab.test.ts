import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import ConfigTab from './ConfigTab.vue'

/**
 * The settings page's config tab (plan 076): the file's own path on the left of
 * the toolbar, 保存并生效 and 刷新 on its right, the editor below it and the
 * page's note under that. There is no title row and no card around the form.
 */
async function mountTab(): Promise<VueWrapper> {
  const wrapper = mount(ConfigTab, { global: { stubs: { FileEditor: true } } })
  await flushPromises()
  return wrapper
}

describe('ConfigTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'configFile').mockResolvedValue({ yaml: 'approval_policy: default\n', path: '/h/forebrain.yaml' })
    vi.spyOn(forebrainApi, 'saveConfigFile').mockResolvedValue({ applied: true, path: '/h/forebrain.yaml' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('opens on the path, the buttons, the editor and the note — no title row', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.find('.forebrain-card').exists()).toBe(false)
    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('[data-testid="config-path"]').text()).toBe('/h/forebrain.yaml')
    expect(toolbar.find('[data-testid="config-path"]').classes()).toContain('font-mono')
    // The save is the primary action; a refresh sits beside it.
    expect(toolbar.find('[data-testid="config-apply"]').text()).toBe('保存并生效')
    expect(toolbar.find('[data-testid="config-apply"]').classes()).toContain('forebrain-btn-primary')
    expect(toolbar.find('[data-testid="config-refresh"]').text()).toContain('刷新')
    expect(wrapper.find('[data-testid="config-note"]').text()).toContain('保存前按运行时的规则校验')
  })

  it('offers the save only once the editor reports a change', async () => {
    const wrapper = await mountTab()
    const apply = wrapper.find('[data-testid="config-apply"]')
    expect(apply.attributes('disabled')).toBeDefined()

    wrapper.findComponent({ name: 'FileEditor' }).vm.$emit('dirty', true)
    await flushPromises()
    expect(wrapper.find('[data-testid="config-apply"]').attributes('disabled')).toBeUndefined()
  })

  it('saves the file and reloads it', async () => {
    const wrapper = await mountTab()
    wrapper.findComponent({ name: 'FileEditor' }).vm.$emit('dirty', true)
    await flushPromises()
    await wrapper.find('[data-testid="config-apply"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.saveConfigFile).toHaveBeenCalledWith('approval_policy: default\n')
    expect(forebrainApi.configFile).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('配置已保存并生效')
  })
})
