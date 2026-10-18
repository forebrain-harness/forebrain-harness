import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import tip from '@/directives/tip'
import { forebrainApi, type McpServerRow, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'

import ProjectMcp from './ProjectMcp.vue'

/**
 * A project's MCP tab (plan 074): the trust gate stands exactly as it did — an
 * untrusted project loads nothing from its directory — and once trusted, the
 * page is the shared three-layer table, listing this project's entries first.
 */
const projectRecord = (trusted = true): ProjectRecord => ({
  id: 'p1',
  name: 'proj',
  icon: '',
  description: '',
  instructions: '',
  root: '/tmp/proj',
  projectKey: 'proj',
  memoryScope: '',
  resourceAccess: false,
  pinned: false,
  createdAt: 0,
  updatedAt: 0,
  trusted,
})

function row(overrides: Partial<McpServerRow> = {}): McpServerRow {
  return {
    name: 'd',
    transport: 'stdio',
    target: 'npx -y dbtool',
    required: false,
    enabled: true,
    startupTimeout: 0,
    requestTimeout: 0,
    headers: {},
    env: {},
    inheritParentEnv: false,
    disableStandaloneSse: false,
    expectedCommandSha256: '',
    defaultToolsApprovalMode: '',
    oauth: {
      mode: '',
      accessToken: '',
      tokenType: '',
      clientId: '',
      clientSecret: '',
      tokenUrl: '',
      authorizationUrl: '',
      redirectUrl: '',
      refreshToken: '',
      scopes: [],
    },
    scope: 'project',
    allowed: true,
    ...overrides,
  }
}

const mounted: VueWrapper[] = []

async function mountTab(trusted: boolean, rows: McpServerRow[] = [row()]): Promise<VueWrapper> {
  vi.mocked(forebrainApi.mcpPage).mockResolvedValue({ rows, total: rows.length, page: 1, pageSize: 10, notApplied: [] })
  const wrapper = mount(ProjectMcp, {
    props: { project: projectRecord(trusted), projectId: 'p1' },
    global: { stubs: { teleport: true }, directives: { tip } },
  })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('ProjectMcp', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'mcpPage')
    vi.spyOn(forebrainApi, 'projectUpdate').mockResolvedValue(projectRecord())
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('holds the trust gate: an untrusted project shows the button and loads nothing', async () => {
    const wrapper = await mountTab(false)

    expect(wrapper.find('[data-testid="project-trust"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="mcp-search"]').exists()).toBe(false)
    expect(forebrainApi.mcpPage).not.toHaveBeenCalled()
  })

  it('draws the three-layer table once the project is trusted', async () => {
    const wrapper = await mountTab(true)

    expect(wrapper.find('[data-testid="project-trust"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="mcp-search"]').exists()).toBe(true)
    // The project view lists project, agent and global: the source filter and
    // the source column are there, and the note is the project's own.
    expect(wrapper.find('[data-testid="mcp-source-filter"]').findAll('option').map((option) => option.text())).toEqual([
      '全部来源',
      '项目',
      '主代理',
      '全局',
    ])
    expect(wrapper.find('[data-testid="mcp-note"]').text()).toContain('这里只增删改项目 MCP')
    expect(vi.mocked(forebrainApi.mcpPage).mock.calls[0][0]).toBe('project')
    expect(vi.mocked(forebrainApi.mcpPage).mock.calls[0][1]).toMatchObject({ projectId: 'p1' })
  })

  it('trusts the project and then reads the table it unlocks', async () => {
    const wrapper = await mountTab(false)

    await wrapper.find('[data-testid="project-trust"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.projectUpdate).toHaveBeenCalledWith('p1', { trust: true })
    expect(wrapper.find('[data-testid="project-trust"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="mcp-search"]').exists()).toBe(true)
    expect(vi.mocked(forebrainApi.mcpPage)).toHaveBeenCalledTimes(1)
  })
})
