import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type SubagentConfigDetail, type ToolMetaRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import { pickOption, selectedText } from '@/test-utils/formControls'
import { InlineTeleport } from '@/test-utils/portal'

import SubagentConfigDrawer from './SubagentConfigDrawer.vue'

/**
 * The subagent drawer (plan 079): one subagent as this primary agent
 * configures it. These tests hold what the form promises — a built-in's prompt
 * is shown in full and only written when it was rewritten, an effort goes with
 * the model chosen beside it, the tool picker never offers subagent_*, 恢复默认
 * clears the rewrite instead of writing a text, a system subagent has nothing
 * to change, and a refusal leaves the drawer open with the server's words.
 */
function detail(overrides: Partial<SubagentConfigDetail> = {}): SubagentConfigDetail {
  return {
    name: 'explore',
    kind: 'builtin',
    description: 'Read-only investigation: searches the code, finds files and call sites',
    modelProvider: '',
    model: '',
    reasoningEffort: '',
    modelSource: '',
    tools: 'read_only',
    allowedTools: [],
    continuable: false,
    enabled: true,
    promptOverridden: false,
    systemPrompt: 'You are the explore subagent.',
    defaultPrompt: 'You are the explore subagent.',
    ...overrides,
  }
}

function tool(name: string): ToolMetaRecord {
  return { name, description: '', category: 'file', readOnly: true }
}

function mountDrawer(props: Record<string, unknown> = {}): VueWrapper {
  return mount(SubagentConfigDrawer, {
    props: { open: true, agentId: 'main', name: 'explore', mode: 'edit', ...props },
    // The drawer's selects are the library's, whose items render through a
    // portal: attached to the document, the shared `pickOption` reaches them
    // the way a person's clicks would.
    attachTo: document.body,
    global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
}

enableAutoUnmount(afterEach)

async function openDrawer(props: Record<string, unknown> = {}): Promise<VueWrapper> {
  const wrapper = mountDrawer(props)
  await flushPromises()
  return wrapper
}

describe('SubagentConfigDrawer', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'subagentConfig').mockResolvedValue(detail())
    vi.spyOn(forebrainApi, 'subagentModelOptions').mockResolvedValue([
      { provider: 'deepseek', model: 'deepseek-v4', label: 'deepseek/deepseek-v4', efforts: ['low', 'medium', 'high'] },
      { provider: 'openai', model: 'gpt-5.4', label: 'openai/gpt-5.4', efforts: ['low', 'high'] },
    ])
    vi.spyOn(forebrainApi, 'toolsPage').mockResolvedValue({
      rows: [tool('read_file'), tool('subagent_run'), tool('grep')],
      categories: ['file'],
      total: 3,
      page: 1,
      pageSize: 100,
    })
    vi.spyOn(forebrainApi, 'updateSubagentConfig').mockResolvedValue({ name: 'explore' })
    vi.spyOn(forebrainApi, 'createSubagentConfig').mockResolvedValue({ name: 'db-migrator' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('shows a built-in prompt in full, word for word', async () => {
    const prompt = 'You are the explore subagent.\n' + '它要回答的四件事写在这里。\n'.repeat(60)
    vi.mocked(forebrainApi.subagentConfig).mockResolvedValue(
      detail({ systemPrompt: prompt, defaultPrompt: 'You are the explore subagent.' }),
    )
    const wrapper = await openDrawer()

    expect(wrapper.find('.forebrain-drawer-head b').text()).toBe('编辑内置子代理 · explore')
    expect((wrapper.find('[data-testid="subagent-prompt"]').element as HTMLTextAreaElement).value).toBe(prompt)
  })

  it('disables the effort while the model follows the primary agent', async () => {
    vi.mocked(forebrainApi.subagentConfig).mockResolvedValue(
      detail({ modelProvider: 'deepseek', model: 'deepseek-v4', reasoningEffort: 'high', modelSource: 'agent' }),
    )
    const wrapper = await openDrawer()

    // Queried afresh each time: the drawer's contents are re-rendered by every
    // change, so a wrapper held across one would point at a stale element.
    const effort = () => wrapper.find('[data-testid="subagent-effort"]')
    expect(effort().attributes('disabled')).toBeUndefined()
    expect(selectedText(effort().element as HTMLElement)).toBe('高')

    await pickOption(wrapper.find('[data-testid="subagent-model"]').element as HTMLElement, '跟随主代理（deepseek/deepseek-v4）')

    // Following the primary agent takes the effort with it, and the box says so.
    expect(effort().attributes('disabled')).toBeDefined()
    expect(selectedText(effort().element as HTMLElement)).toBe('跟随主代理')
  })

  it('offers a chosen tool set without the subagent_* family', async () => {
    vi.mocked(forebrainApi.subagentConfig).mockResolvedValue(
      detail({ name: 'db-migrator', kind: 'custom', defaultPrompt: undefined, systemPrompt: 'Migrate the database.' }),
    )
    const wrapper = await openDrawer({ name: 'db-migrator' })

    expect(wrapper.find('[data-testid="subagent-picker"]').exists()).toBe(false)
    await wrapper.find('[role="radio"][value="pick"]').trigger('click')
    await flushPromises()

    const picker = wrapper.find('[data-testid="subagent-picker"]')
    expect(picker.exists()).toBe(true)
    expect(picker.findAll('.sa-chk span').map((name) => name.text())).toEqual(['read_file', 'grep'])
    expect(picker.text()).not.toContain('subagent_run')
    expect(picker.find('[data-testid="subagent-pick-note"]').text()).toContain('已选 0 个')
  })

  it('opens a subagent that already picks its tools with that set chosen', async () => {
    vi.mocked(forebrainApi.subagentConfig).mockResolvedValue(
      detail({ name: 'db-migrator', kind: 'custom', defaultPrompt: undefined, tools: 'pick', allowedTools: ['grep'] }),
    )
    const wrapper = await openDrawer({ name: 'db-migrator' })
    await flushPromises()

    const picker = wrapper.find('[data-testid="subagent-picker"]')
    expect(picker.exists()).toBe(true)
    expect(picker.find('[data-testid="subagent-pick-note"]').text()).toContain('已选 1 个')
    const chosen = wrapper
      .findAll('.sa-chk')
      .filter((cell) => cell.find('[role="checkbox"]').attributes('aria-checked') === 'true')
    expect(chosen.map((cell) => cell.text())).toEqual(['grep'])

    await wrapper.find('[data-testid="subagent-save"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.updateSubagentConfig).toHaveBeenCalledWith(
      'main',
      'db-migrator',
      expect.objectContaining({ tools: 'pick', allowedTools: ['grep'] }),
    )
  })

  it('clears the rewrite rather than writing it back, once 恢复默认 is pressed', async () => {
    vi.mocked(forebrainApi.subagentConfig).mockResolvedValue(
      detail({ promptOverridden: true, systemPrompt: 'MY OWN EXPLORE', defaultPrompt: 'THE HARNESS ONE' }),
    )
    const wrapper = await openDrawer()

    expect(wrapper.find('[data-testid="subagent-overridden"]').text()).toBe('已改写')
    await wrapper.find('[data-testid="subagent-restore"]').trigger('click')

    expect((wrapper.find('[data-testid="subagent-prompt"]').element as HTMLTextAreaElement).value).toBe('THE HARNESS ONE')
    expect(wrapper.find('[data-testid="subagent-overridden"]').exists()).toBe(false)

    await wrapper.find('[data-testid="subagent-save"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.updateSubagentConfig).toHaveBeenCalledWith('main', 'explore', {
      model: '',
      reasoningEffort: '',
      resetPrompt: true,
    })
    expect(wrapper.emitted('saved')).toEqual([['explore']])
  })

  it('writes a built-in prompt only when it was rewritten here', async () => {
    const wrapper = await openDrawer()

    await wrapper.find('[data-testid="subagent-save"]').trigger('click')
    await flushPromises()

    // An untouched prompt travels not at all: writing it would pin the text as
    // this agent's rewrite of a built-in that still follows the harness.
    expect(forebrainApi.updateSubagentConfig).toHaveBeenCalledWith('main', 'explore', {
      model: '',
      reasoningEffort: '',
    })
  })

  it('draws a system subagent with nothing to change and one way out', async () => {
    vi.mocked(forebrainApi.subagentConfig).mockResolvedValue(
      detail({ name: 'guardian', kind: 'system', tools: 'none', enabled: null, systemPrompt: 'You are the guardian.' }),
    )
    const wrapper = await openDrawer({ name: 'guardian', mode: 'view' })

    expect(wrapper.find('.forebrain-drawer-head b').text()).toBe('查看系统子代理 · guardian')
    expect(wrapper.findAll('input').every((input) => input.attributes('readonly') !== undefined)).toBe(true)
    // Both dropdowns are the library's triggers, and both are off: nothing here
    // is this page's to change.
    const triggers = wrapper.findAll('[data-testid="subagent-model"], [data-testid="subagent-effort"]')
    expect(triggers).toHaveLength(2)
    expect(triggers.every((trigger) => trigger.attributes('disabled') !== undefined)).toBe(true)
    expect(wrapper.find('textarea').attributes('readonly')).toBeDefined()
    expect((wrapper.find('[data-testid="subagent-prompt"]').element as HTMLTextAreaElement).value).toBe('You are the guardian.')
    // 何时调用 and the tool tier are the harness's facts about it.
    expect((wrapper.find('[data-testid="subagent-when"]').element as HTMLInputElement).value).toBe('审批模式开启自动审查时')
    expect((wrapper.find('[data-testid="subagent-tools-fixed"]').element as HTMLInputElement).value).toBe('无工具（内置，不能改）')
    expect(wrapper.find('[data-testid="subagent-note"]').text()).toBe('系统子代理只能查看。')

    const foot = wrapper.find('.forebrain-drawer-foot')
    expect(foot.findAll('button').map((button) => button.text())).toEqual(['关闭'])
  })

  it('keeps the drawer open and shows the server text when a create is refused', async () => {
    vi.mocked(forebrainApi.createSubagentConfig).mockRejectedValue(
      new Error('a subagent named "explore" is already configured for this agent'),
    )
    const wrapper = await openDrawer({ mode: 'create', name: undefined })

    expect(wrapper.find('.forebrain-drawer-head b').text()).toBe('新建子代理')
    await wrapper.find('[data-testid="subagent-name"]').setValue('explore')
    await wrapper.find('[data-testid="subagent-save"]').trigger('click')
    await flushPromises()

    const block = wrapper.find('[data-testid="subagent-error"]')
    expect(block.text()).toContain('没有保存：')
    expect(block.text()).toContain('a subagent named "explore" is already configured for this agent')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.find('.forebrain-drawer').exists()).toBe(true)
    // The form is still there, and the save can be tried again.
    expect((wrapper.find('[data-testid="subagent-name"]').element as HTMLInputElement).value).toBe('explore')
    expect(wrapper.find('[data-testid="subagent-save"]').attributes('disabled')).toBeUndefined()
  })

  it('refuses a name the server would refuse, before anything is sent', async () => {
    const wrapper = await openDrawer({ mode: 'create', name: undefined })

    await wrapper.find('[data-testid="subagent-name"]').setValue('DB_Migrator')
    expect(wrapper.find('[data-testid="subagent-name-invalid"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="subagent-save"]').attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="subagent-name"]').setValue('db-migrator')
    expect(wrapper.find('[data-testid="subagent-name-invalid"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="subagent-save"]').attributes('disabled')).toBeUndefined()
  })

  it('writes an empty effort tier as the empty string, not the select’s marker', async () => {
    vi.mocked(forebrainApi.subagentConfig).mockResolvedValue(
      detail({ modelProvider: 'deepseek', model: 'deepseek-v4', reasoningEffort: 'high', modelSource: 'agent' }),
    )
    const wrapper = await openDrawer()

    // 跟随模型默认 is the empty string on the wire: a marker that no real value
    // can equal stands in for it inside the list, and the form field — which is
    // what the save carries — goes back to ''.
    const effort = wrapper.find('[data-testid="subagent-effort"]')
    await pickOption(effort.element as HTMLElement, '跟随模型默认')
    expect(selectedText(wrapper.find('[data-testid="subagent-effort"]').element as HTMLElement)).toBe('跟随模型默认')

    await wrapper.find('[data-testid="subagent-save"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.updateSubagentConfig).toHaveBeenCalledWith('main', 'explore', {
      model: 'deepseek/deepseek-v4',
      reasoningEffort: '',
    })
  })

  it('keeps the run mode a boolean although the select speaks in words', async () => {
    const wrapper = await openDrawer({ mode: 'create', name: undefined })

    await wrapper.find('[data-testid="subagent-name"]').setValue('db-migrator')
    await pickOption(wrapper.find('[data-testid="subagent-mode"]').element as HTMLElement, '完成即结束')

    await wrapper.find('[data-testid="subagent-save"]').trigger('click')
    await flushPromises()

    // The option's own value is the word `off`; the form field — and the body
    // the server reads — stay boolean.
    expect(forebrainApi.createSubagentConfig).toHaveBeenCalledWith(
      'main',
      expect.objectContaining({ name: 'db-migrator', continuable: false }),
    )
  })
})
