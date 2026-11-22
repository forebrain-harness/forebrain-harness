import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, onMounted } from 'vue'

import tip from '@/directives/tip'
import { forebrainApi, type LlmModelChoice, type SharedSubagentDetail, type SharedSubagentListing } from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'
import { useSettingsForm } from '@/components/settings/form/useSettingsForm'

import GlobalSubagentsGroup from './GlobalSubagentsGroup.vue'

/**
 * 设置 · 子代理 · 「全局共享子代理」(plan 314): one install-wide registry with
 * create/edit/delete, and the four built-in types' default model rows beside it.
 * Neither half carries a per-agent switch — enablement belongs to an agent's own
 * page.
 */
const mounted: VueWrapper[] = []

function sharedRow(overrides: Partial<SharedSubagentDetail> = {}): SharedSubagentDetail {
  return {
    name: 'repo-auditor',
    kind: 'shared',
    description: '审查一个仓库区域的改动',
    modelProvider: 'deepseek',
    model: 'deepseek-v4-flash',
    reasoningEffort: 'high',
    modelSource: 'global',
    tools: 'read_only',
    allowedTools: [],
    continuable: true,
    // A registry row carries no switch: enablement is per agent.
    enabled: null,
    promptOverridden: false,
    systemPrompt: '你审查一个仓库区域。',
    ...overrides,
  }
}

function listing(overrides: Partial<SharedSubagentListing> = {}): SharedSubagentListing {
  return {
    rows: [sharedRow()],
    types: [
      { type: 'general-purpose', model: '', reasoningEffort: '' },
      { type: 'explore', model: 'llm-row-1', reasoningEffort: 'high' },
      { type: 'plan', model: '', reasoningEffort: '' },
      { type: 'verification', model: '', reasoningEffort: '' },
    ],
    ...overrides,
  }
}

const choices: LlmModelChoice[] = [
  {
    id: 'llm-row-1',
    provider: 'openai',
    providerName: 'OpenAI',
    model: 'gpt-main',
    baseUrl: 'https://api.openai.test/v1',
    apiPath: '/chat/completions',
    endpoint: 'https://api.openai.test/v1/chat/completions',
    label: 'openai/gpt-main',
    efforts: ['low', 'high'],
    defaultEffort: 'low',
    supportsImages: false,
  },
]

async function mountGroup(): Promise<VueWrapper> {
  const Host = defineComponent({
    components: { UnderTest: GlobalSubagentsGroup },
    setup() {
      const form = useSettingsForm()
      onMounted(() => void form.load())
      return { form }
    },
    template: '<UnderTest :form="form" />',
  })
  const wrapper = mount(Host, {
    attachTo: document.body,
    global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
  mounted.push(wrapper)
  await flushPromises()
  await wrapper.vm.$nextTick()
  return wrapper
}

describe('GlobalSubagentsGroup', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'fetchSharedSubagents').mockResolvedValue(listing())
    vi.spyOn(forebrainApi, 'settings').mockResolvedValue({ values: {}, secrets: {} } as never)
    vi.spyOn(forebrainApi, 'llmModelChoices').mockResolvedValue(choices)
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('lists the registry and the four per-type rows, with no per-agent switch', async () => {
    const wrapper = await mountGroup()

    expect(wrapper.find('[data-group="shared-subagents"] h3').text()).toBe('全局共享子代理')
    expect(vi.mocked(forebrainApi.fetchSharedSubagents)).toHaveBeenCalledTimes(1)

    const row = wrapper.find('[data-shared-row="repo-auditor"]')
    expect(row.exists()).toBe(true)
    expect(row.text()).toContain('审查一个仓库区域的改动')
    expect(row.text()).toContain('deepseek/deepseek-v4-flash')
    expect(row.text()).toContain('只读')
    // The registry is edited and deleted here, and switched nowhere.
    expect(row.find('[data-testid="shared-subagent-edit-repo-auditor"]').exists()).toBe(true)
    expect(row.find('[data-testid="shared-subagent-delete-repo-auditor"]').exists()).toBe(true)
    expect(row.find('[role="switch"]').exists()).toBe(false)

    // The four public types are the same group's second half.
    expect(wrapper.findAll('[data-shared-type]').map((element) => element.attributes('data-shared-type'))).toEqual([
      'general-purpose',
      'explore',
      'plan',
      'verification',
    ])
    // A type with a global row shows it; one without says nothing is set.
    const explore = wrapper.find('[data-shared-type="explore"]')
    expect(explore.find('[data-goal-evaluator-model="llm-row-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-shared-type="plan"] [data-goal-evaluator-model=""]').exists()).toBe(true)
  })

  it('saves one type’s default row through the registry endpoint', async () => {
    const update = vi.spyOn(forebrainApi, 'updateSubagentTypeDefault').mockResolvedValue(
      listing({ types: [{ type: 'general-purpose', model: 'llm-row-1', reasoningEffort: 'low' }, ...listing().types.slice(1)] }),
    )
    const wrapper = await mountGroup()

    // Nothing moved yet, so the row's save is unavailable.
    expect(wrapper.find('[data-shared-type-save="general-purpose"]').attributes('disabled')).toBeDefined()

    // The picker is an inline one — its list stays open until a row is chosen —
    // so the choice is the option inside this type's own row.
    await wrapper.find('[data-shared-type="general-purpose"] [data-goal-evaluator-option="llm-row-1"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-shared-type-save="general-purpose"]').attributes('disabled')).toBeUndefined()
    await wrapper.find('[data-shared-type-save="general-purpose"]').trigger('click')
    await flushPromises()

    expect(update).toHaveBeenCalledWith('general-purpose', { model: 'llm-row-1', reasoningEffort: '' })
    expect(wrapper.find('[data-shared-type-saved="general-purpose"]').exists()).toBe(true)
  })

  it('creates a shared subagent in the global drawer and reloads the registry', async () => {
    vi.spyOn(forebrainApi, 'createSharedSubagent').mockResolvedValue(listing())
    const wrapper = await mountGroup()

    await wrapper.find('[data-testid="shared-subagents-new"]').trigger('click')
    await flushPromises()

    // The drawer names the shared scope, and it has no agent behind it.
    expect(wrapper.find('[role="dialog"]').text()).toContain('新建共享子代理')
    expect(wrapper.find('[data-testid="subagent-name"]').element as HTMLInputElement).toBeTruthy()

    await wrapper.find('[data-testid="subagent-name"]').setValue('repo-scout')
    await wrapper.find('[data-testid="subagent-description"]').setValue('finds the files a task touches')
    await wrapper.find('[data-testid="subagent-prompt"]').setValue('You find files.')
    await wrapper.find('[data-testid="subagent-save"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.createSharedSubagent)).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'repo-scout', description: 'finds the files a task touches', systemPrompt: 'You find files.' }),
    )
    expect(vi.mocked(forebrainApi.fetchSharedSubagents)).toHaveBeenCalledTimes(2)
  })

  it('edits a definition through the global put and renders a refusal inline', async () => {
    vi.spyOn(forebrainApi, 'updateSharedSubagent').mockRejectedValue(new Error('description required'))
    const wrapper = await mountGroup()

    await wrapper.find('[data-testid="shared-subagent-edit-repo-auditor"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').text()).toContain('编辑共享子代理 repo-auditor')

    await wrapper.find('[data-testid="subagent-description"]').setValue('')
    await wrapper.find('[data-testid="subagent-save"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.updateSharedSubagent)).toHaveBeenCalledWith('repo-auditor', expect.objectContaining({ description: '' }))
    expect(wrapper.find('[role="dialog"]').text()).toContain('description required')
  })

  it('asks before deleting, and deletes the definition alone', async () => {
    vi.mocked(forebrainApi.fetchSharedSubagents)
      .mockResolvedValueOnce(listing())
      .mockResolvedValue(listing({ rows: [] }))
    const remove = vi.spyOn(forebrainApi, 'deleteSharedSubagent').mockResolvedValue(listing({ rows: [] }))
    const wrapper = await mountGroup()

    await wrapper.find('[data-testid="shared-subagent-delete-repo-auditor"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[role="dialog"] .forebrain-modal-title').text()).toBe('删除共享子代理 repo-auditor？')
    expect(remove).not.toHaveBeenCalled()

    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()

    expect(remove).toHaveBeenCalledWith('repo-auditor')
    expect(wrapper.find('[data-shared-empty]').exists()).toBe(true)
  })

  it('renders an empty registry as the sentence that says how to start one', async () => {
    vi.mocked(forebrainApi.fetchSharedSubagents).mockResolvedValue(listing({ rows: [] }))
    const wrapper = await mountGroup()

    expect(wrapper.find('[data-shared-empty]').text()).toBe(
      '还没有共享子代理。新建一个之后，每个主代理都能在自己的子代理页启用它。',
    )
  })
})
