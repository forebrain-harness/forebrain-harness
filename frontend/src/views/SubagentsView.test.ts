import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type SubagentConfigRow } from '@/lib/api'
import { setLocale } from '@/locales'
import { openSelect, pickOption } from '@/test-utils/formControls'
import { InlineTeleport } from '@/test-utils/portal'

import SubagentsView from './SubagentsView.vue'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    forebrainApi: {
      ...actual.forebrainApi,
      subagentConfigPage: vi.fn(),
      setSubagentEnabled: vi.fn(),
      setAgentSubagentSwitch: vi.fn(),
      deleteSubagentConfig: vi.fn(),
    },
  }
})

// The page lists one primary agent's subagents, keyed by its id: the tenant the
// shell is on is the only thing the composable contributes here.
vi.mock('@/composables/usePrimaryAgents', async () => {
  const { ref } = await import('vue')
  return {
    usePrimaryAgents: () => ({
      activeId: ref('main'),
      records: ref([]),
      active: ref(null),
      loading: ref(false),
      error: ref(''),
      refreshToken: ref(0),
    }),
  }
})

/**
 * The subagent configuration page (plan 079): five columns, no status column, the
 * row switch first in the action column for the two kinds that have one, the
 * agent's own switch in the toolbar, and a delete that asks before it writes.
 */
const mounted: VueWrapper[] = []

function custom(overrides: Partial<SubagentConfigRow> = {}): SubagentConfigRow {
  return {
    name: 'db-migrator',
    kind: 'custom',
    description: '写数据库迁移，并在临时库上跑一遍升级与回滚',
    modelProvider: '',
    model: '',
    reasoningEffort: '',
    modelSource: '',
    tools: 'pick',
    allowedTools: ['read_file', 'write_file'],
    continuable: true,
    enabled: true,
    promptOverridden: false,
    ...overrides,
  }
}

function builtin(overrides: Partial<SubagentConfigRow> = {}): SubagentConfigRow {
  return {
    name: 'explore',
    kind: 'builtin',
    description: 'Fast read-only investigation',
    modelProvider: 'deepseek',
    model: 'deepseek-v4',
    reasoningEffort: 'high',
    modelSource: 'global',
    tools: 'read_only',
    allowedTools: [],
    continuable: true,
    enabled: true,
    promptOverridden: true,
    ...overrides,
  }
}

/** A globally shared custom subagent (plan 314): an install-wide definition this
 *  agent may dispatch, switched here and edited in 设置 · 子代理. */
function shared(overrides: Partial<SubagentConfigRow> = {}): SubagentConfigRow {
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
    // Default-disabled: the registry's definition is nobody's until this agent
    // enables it.
    enabled: false,
    promptOverridden: false,
    ...overrides,
  }
}

function system(overrides: Partial<SubagentConfigRow> = {}): SubagentConfigRow {
  return {
    name: 'plan-reviewer',
    kind: 'system',
    description: 'Second opinion before a plan is approved',
    modelProvider: '',
    model: '',
    reasoningEffort: '',
    modelSource: '',
    tools: 'read_only',
    allowedTools: [],
    continuable: false,
    enabled: null,
    promptOverridden: false,
    ...overrides,
  }
}

async function mountPage(): Promise<VueWrapper> {
  const wrapper = mount(SubagentsView, {
    // The toolbar's 类型 filter is the library's Select, whose items render
    // through a portal: attached to the document, the shared helpers reach them
    // the way a person's clicks would.
    attachTo: document.body,
    global: { stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('SubagentsView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.mocked(forebrainApi.subagentConfigPage).mockResolvedValue({
      rows: [custom(), builtin(), shared(), system()],
      total: 4,
      page: 1,
      pageSize: 10,
      switch: false,
      global: false,
    })
    vi.mocked(forebrainApi.setAgentSubagentSwitch).mockResolvedValue({ switch: true })
    vi.mocked(forebrainApi.setSubagentEnabled).mockResolvedValue({ switch: true })
    vi.mocked(forebrainApi.deleteSubagentConfig).mockResolvedValue({ name: 'db-migrator' })
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.clearAllMocks()
  })

  it('reads this agent’s subagents and opens on five columns, with no status column', async () => {
    const wrapper = await mountPage()

    expect(wrapper.find('h2').exists()).toBe(false)
    expect(vi.mocked(forebrainApi.subagentConfigPage)).toHaveBeenCalledWith('main', expect.objectContaining({ page: 1, pageSize: 10 }))

    const heads = wrapper.findAll('thead th')
    expect(heads.map((head) => head.text())).toEqual(['子代理', '类型', '模型', '工具', '操作'])
    expect(heads).toHaveLength(5)
    // 操作 is centred over its column (plan 079 step 9).
    expect(heads[4].classes()).toContain('text-center')
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 4 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe(
      '“内置”与“自定义”的子代理启用后由模型按说明派发；“系统”子代理在计划审阅、/goal、自动审批等场合由系统调用，只能查看。修改已保存，新会话生效。',
    )
    // Scoped to the list this trigger opened rather than the document-wide
    // `optionTexts()`: the pager draws a page-size select too, and both render
    // their items inline here, so the document holds options of a select
    // nobody opened.
    await openSelect(wrapper.find('[data-testid="subagents-kind-filter"]').element as HTMLElement)
    const list = wrapper.find('[role="listbox"][data-state="open"]')
    expect(list.findAll('[role="option"]').map((option) => option.text())).toEqual([
      '全部类型',
      '内置',
      '自定义',
      '共享',
      '系统',
    ])
  })

  it('narrows the list by kind on the server', async () => {
    const wrapper = await mountPage()

    await pickOption(wrapper.find('[data-testid="subagents-kind-filter"]').element as HTMLElement, '系统')
    await flushPromises()

    expect(vi.mocked(forebrainApi.subagentConfigPage).mock.calls[1][1]).toMatchObject({ kind: 'system', page: 1 })
  })

  it('draws the kind, the model with its effort, the tools and the page’s own description', async () => {
    const wrapper = await mountPage()

    const customRow = wrapper.find('[data-subagent-row="db-migrator"]')
    expect(customRow.find('.forebrain-badge').text()).toBe('自定义')
    expect(customRow.text()).toContain('写数据库迁移，并在临时库上跑一遍升级与回滚')
    expect(customRow.text()).toContain('指定 2 个')
    expect(customRow.text()).toContain('跟随主代理')

    const builtinRow = wrapper.find('[data-subagent-row="explore"]')
    // The kind badge sits in its own column; the first badge in the row is the
    // 已改写 one beside the name.
    expect(builtinRow.findAll('td')[1].find('.forebrain-badge').text()).toBe('内置')
    // The harness's own description is said in the page's words.
    expect(builtinRow.text()).toContain('只读调查：快速搜索代码，找文件与调用点')
    expect(builtinRow.text()).toContain('deepseek/deepseek-v4')
    expect(builtinRow.text()).toContain('高')
    expect(builtinRow.text()).toContain('只读')
    expect(builtinRow.find('[data-testid="subagent-overridden-explore"]').text()).toBe('已改写')

    const systemRow = wrapper.find('[data-subagent-row="plan-reviewer"]')
    expect(systemRow.find('.forebrain-badge').text()).toBe('系统')
    expect(systemRow.text()).toContain('审批时选择')
    expect(systemRow.find('[data-testid="subagent-overridden-plan-reviewer"]').exists()).toBe(false)
  })

  // The reviewer's install-wide row is the approval picker's default (plan 313):
  // a configured row shows as the model and the level it carries, and a type
  // without one keeps the page's own words.
  it('shows the plan reviewer’s install-wide row, and only that row', async () => {
    vi.mocked(forebrainApi.subagentConfigPage).mockResolvedValue({
      rows: [
        custom(),
        builtin(),
        system({ globalModelLabel: 'deepseek/deepseek-v4-pro', globalReasoningEffort: 'high' }),
        system({ name: 'goal-evaluator', description: 'Checks the goal' }),
      ],
      total: 4,
      page: 1,
      pageSize: 10,
      switch: false,
      global: false,
    })
    const wrapper = await mountPage()

    const reviewer = wrapper.find('[data-subagent-row="plan-reviewer"]')
    expect(reviewer.text()).toContain('deepseek/deepseek-v4-pro')
    // The level reads through the page's one effort table, on one line.
    expect(reviewer.text()).toContain('高')
    expect(reviewer.text()).not.toContain('审批时选择')

    // A system type with no install-wide row keeps the fallback wording.
    expect(wrapper.find('[data-subagent-row="goal-evaluator"]').text()).toContain('跟随主代理')
  })

  it('gives each kind its own action column, the switch first and the icons after it', async () => {
    const wrapper = await mountPage()
    const labels = (name: string) =>
      wrapper
        .find(`[data-subagent-row="${name}"]`)
        .findAll('.forebrain-row-action')
        .map((button) => button.attributes('aria-label'))

    // 自定义: switch on the left, then 编辑 and 删除.
    const customRow = wrapper.find('[data-subagent-row="db-migrator"]')
    expect(customRow.find('[data-testid="subagent-switch-db-migrator"]').exists()).toBe(true)
    expect(labels('db-migrator')).toEqual(['编辑 db-migrator', '删除 db-migrator'])
    expect(customRow.find('.forebrain-icon-slot').exists()).toBe(false)

    // 内置: always on (P5) — no switch where the switchable rows carry one,
    // and 编辑 beside the hole where 删除 would be.
    const builtinRow = wrapper.find('[data-subagent-row="explore"]')
    expect(builtinRow.find('[data-testid="subagent-switch-explore"]').exists()).toBe(false)
    expect(builtinRow.find('.sa-sw-slot').exists()).toBe(true)
    expect(labels('explore')).toEqual(['编辑 explore'])
    expect(builtinRow.find('.forebrain-icon-slot').exists()).toBe(true)

    // 共享: the switch, and 查看 only — the definition belongs to the install-wide
    // registry, so this page has neither 编辑 nor 删除 (plan 314).
    const sharedRow = wrapper.find('[data-subagent-row="repo-auditor"]')
    expect(sharedRow.find('[data-testid="subagent-switch-repo-auditor"]').exists()).toBe(true)
    expect(labels('repo-auditor')).toEqual(['查看 repo-auditor'])
    expect(sharedRow.find('.forebrain-badge').text()).toBe('共享')
    expect(sharedRow.text()).toContain('审查一个仓库区域的改动')

    // 系统: no switch at all — a same-width hole keeps the icons aligned — and 查看 only.
    const systemRow = wrapper.find('[data-subagent-row="plan-reviewer"]')
    expect(systemRow.find('[data-testid="subagent-switch-plan-reviewer"]').exists()).toBe(false)
    expect(systemRow.find('.sa-sw-slot').exists()).toBe(true)
    expect(labels('plan-reviewer')).toEqual(['查看 plan-reviewer'])
  })

  it('switches this agent’s own subagent feature from the toolbar, then reloads', async () => {
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="subagents-switch"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.setAgentSubagentSwitch)).toHaveBeenCalledWith('main', true)
    expect(vi.mocked(forebrainApi.subagentConfigPage)).toHaveBeenCalledTimes(2)
  })

  it('switches one row and reloads, because the toolbar’s switch may move with it', async () => {
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="subagent-switch-db-migrator"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.setSubagentEnabled)).toHaveBeenCalledWith('main', 'db-migrator', false)
    expect(vi.mocked(forebrainApi.subagentConfigPage)).toHaveBeenCalledTimes(2)
  })

  it('asks before deleting a custom subagent', async () => {
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="subagent-delete-db-migrator"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[role="dialog"] .forebrain-modal-title').text()).toBe('删除子代理 db-migrator？')
    expect(vi.mocked(forebrainApi.deleteSubagentConfig)).not.toHaveBeenCalled()

    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(forebrainApi.deleteSubagentConfig)).toHaveBeenCalledWith('main', 'db-migrator')
    expect(vi.mocked(forebrainApi.subagentConfigPage)).toHaveBeenCalledTimes(2)
  })
})
