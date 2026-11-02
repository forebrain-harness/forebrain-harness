import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'
import ConfigTab from './ConfigTab.vue'

/**
 * The settings page's config tab (plan 191 §0.19): the file's own path on the
 * left of the toolbar, 保存 and 刷新 on its right, the editor below it and the
 * note under that. The file is saved as written unless it is not YAML at all —
 * then nothing is written and the line is named above the editor.
 */
// The editor is stubbed with a component that declares the one prop this file
// is responsible for handing over, so the wiring is what the test sees.
const EditorStub = defineComponent({ props: ['lint', 'modelValue'], template: '<div data-testid="editor" />' })

async function mountTab(): Promise<VueWrapper> {
  const wrapper = mount(ConfigTab, { global: { stubs: { FileEditor: EditorStub } } })
  await flushPromises()
  return wrapper
}

describe('ConfigTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'configFile').mockResolvedValue({
      yaml: 'gateway:\n  http_addr: 127.0.0.1:8787\n',
      path: '/h/forebrain.yaml',
    })
    vi.spyOn(forebrainApi, 'configFileSchema').mockResolvedValue({
      keys: ['gateway.http_addr', 'gateway.auth.token', 'credentials.chatgpt'],
    })
    vi.spyOn(forebrainApi, 'saveConfigFile').mockResolvedValue({ saved: true })
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
    expect(toolbar.find('[data-testid="config-apply"]').text()).toBe('保存')
    expect(toolbar.find('[data-testid="config-apply"]').classes()).toContain('forebrain-btn-primary')
    expect(toolbar.find('[data-testid="config-refresh"]').text()).toContain('刷新')
    expect(wrapper.find('[data-testid="config-note"]').text()).toBe('改动重启后生效')
  })

  it('offers the save only once the editor reports a change', async () => {
    const wrapper = await mountTab()
    const apply = wrapper.find('[data-testid="config-apply"]')
    expect(apply.attributes('disabled')).toBeDefined()

    wrapper.findComponent(EditorStub).vm.$emit('dirty', true)
    await flushPromises()
    expect(wrapper.find('[data-testid="config-apply"]').attributes('disabled')).toBeUndefined()
  })

  it('saves the file and says it takes effect after a restart', async () => {
    const wrapper = await mountTab()
    wrapper.findComponent(EditorStub).vm.$emit('dirty', true)
    await flushPromises()
    await wrapper.find('[data-testid="config-apply"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.saveConfigFile).toHaveBeenCalledWith('gateway:\n  http_addr: 127.0.0.1:8787\n')
    expect(forebrainApi.configFile).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="config-notice"]').text()).toBe('已保存，重启后生效')
  })

  it('refuses a syntax error without writing, naming the line above the editor', async () => {
    vi.mocked(forebrainApi.saveConfigFile).mockRejectedValue({
      response: { status: 400, data: { line: 3, message: '缺少冒号' } },
    })
    const wrapper = await mountTab()
    wrapper.findComponent(EditorStub).vm.$emit('dirty', true)
    await flushPromises()
    await wrapper.find('[data-testid="config-apply"]').trigger('click')
    await flushPromises()

    const banner = wrapper.find('[data-testid="config-unsaved"]')
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain('没有保存')
    expect(banner.text()).toContain('第 3 行缺少冒号')
    // A refusal keeps the file as it was: no reload, no saved notice.
    expect(wrapper.find('[data-testid="config-notice"]').exists()).toBe(false)
  })

  it('hands the editor the whitelist it underlines against', async () => {
    const wrapper = await mountTab()
    const editor = wrapper.findComponent(EditorStub)
    expect(editor.props('lint')).toBeTypeOf('function')
    expect(editor.props('lint')('memories: 8\ngateway:\n  http_addr "x"\n')).toEqual([
      { line: 0, message: '不存在' },
      { line: 2, message: '缺少冒号' },
    ])
  })
})
