import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type PermissionRuleRecord, type ToolMetaRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'
import PermissionsView from './PermissionsView.vue'

/**
 * The permissions page (plan 073): no title row — the top bar names the page —
 * a toolbar whose left is the search box and whose right holds the two buttons
 * the title row used to carry, a fixed table of the rules with icon actions,
 * a pager under it and the page's note below that.
 */
const mounted: VueWrapper[] = []

function rule(overrides: Partial<PermissionRuleRecord> = {}): PermissionRuleRecord {
  return {
    source: 'localSettings',
    behavior: 'allow',
    toolName: 'shell',
    ruleContent: 'git status',
    rule: { toolName: 'shell', ruleContent: 'git status' },
    ...overrides,
  }
}

function tool(name: string): ToolMetaRecord {
  return { name, description: '', category: 'shell', readOnly: false, destructive: false }
}

async function mountPage(): Promise<VueWrapper> {
  const wrapper = mount(PermissionsView, { global: { directives: { tip }, stubs: { teleport: InlineTeleport } } })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('PermissionsView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'permissionsRulesPage').mockResolvedValue({
      rows: [
        rule(),
        rule({ behavior: 'ask', toolName: 'web_fetch', ruleContent: 'domain:internal.example.com' }),
        rule({ behavior: 'deny', toolName: 'read_file', ruleContent: '**/.env' }),
      ],
      mode: 'on-request',
      total: 3,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'toolsList').mockResolvedValue([tool('shell'), tool('read_file')])
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('opens on a search toolbar, the table, the pager and the note — no title row', async () => {
    const wrapper = await mountPage()

    expect(wrapper.find('h2').exists()).toBe(false)

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('input[type="search"]').attributes('placeholder')).toBe('搜索规则内容')
    expect(toolbar.find('[data-testid="permissions-verify"]').text()).toBe('判定验证')
    expect(toolbar.find('[data-testid="add-rule"]').text()).toBe('添加规则')

    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="agent-rule"]')).toHaveLength(3)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 3 条')

    const note = wrapper.find('p.forebrain-list-note')
    expect(note.text()).toContain('本主代理已记住的授权规则，写在')
    expect(note.find('span.font-mono').text()).toBe('state/permissions/local_settings.json')
    expect(note.text()).toContain('默认审批模式在“设置 → 审批默认”；项目规则在各项目空间；单个会话可在输入框旁临时切换。')

    // The agent's own rules only: the filter is the server's.
    expect(vi.mocked(forebrainApi.permissionsRulesPage).mock.calls[0][0]).toMatchObject({
      page: 1,
      pageSize: 10,
      source: 'localSettings',
    })
  })

  it('paints each rule’s behaviour as its badge, with the tool and the rule text in monospace', async () => {
    const wrapper = await mountPage()

    const rows = wrapper.findAll('[data-testid="agent-rule"]')
    expect(rows.map((row) => row.find('.forebrain-badge').text())).toEqual(['允许', '询问', '拒绝'])
    expect(rows.map((row) => row.find('.forebrain-badge').classes().join(' '))).toEqual([
      'forebrain-badge forebrain-badge--success',
      'forebrain-badge forebrain-badge--warning',
      'forebrain-badge forebrain-badge--error',
    ])

    const cells = rows[2].findAll('td')
    expect(cells[1].find('.forebrain-cell-1').text()).toBe('read_file')
    expect(cells[1].find('.forebrain-cell-1').classes()).toContain('font-mono')
    expect(cells[2].find('.forebrain-cell-1').text()).toBe('**/.env')
    expect(cells[2].find('.forebrain-cell-1').classes()).toContain('font-mono')
  })

  it('offers the row actions as icon buttons that name the rule, never a text button', async () => {
    const wrapper = await mountPage()

    expect(wrapper.findAll('.forebrain-table button.forebrain-btn')).toHaveLength(0)
    const first = wrapper.findAll('[data-testid="agent-rule"]')[0]
    expect(first.findAll('.forebrain-row-action').map((button) => button.attributes('aria-label'))).toEqual([
      '编辑 git status',
      '删除 git status',
    ])
  })

  it('searches and pages on the server, reloading the page it wrote from', async () => {
    vi.spyOn(forebrainApi, 'permissionsUpdate').mockResolvedValue({ ok: true })

    const wrapper = await mountPage()
    const page = vi.mocked(forebrainApi.permissionsRulesPage)

    await wrapper.find('[data-testid="permissions-search"]').setValue('git')
    await new Promise((resolve) => setTimeout(resolve, 350))
    await flushPromises()
    expect(page.mock.calls[1][0]).toMatchObject({ q: 'git', page: 1, source: 'localSettings' })

    await wrapper.findAll('[data-testid="remove-rule"]')[0].trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(page.mock.calls.length).toBeGreaterThan(2)
  })
})
