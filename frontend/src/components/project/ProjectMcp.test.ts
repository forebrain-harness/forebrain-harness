import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { forebrainApi, type ProjectMcpRecord, type ProjectRecord } from '@/lib/api'
import ProjectMcp from './ProjectMcp.vue'

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
  archivedAt: 0,
  createdAt: 0,
  updatedAt: 0,
  trusted,
})

const recordOf = (overrides: Partial<ProjectMcpRecord> = {}): ProjectMcpRecord => ({
  servers: [{ name: 'globaltool', transport: 'stdio', urlSet: false, scope: 'global' }],
  overriddenGlobal: [],
  notApplied: [],
  pendingConsent: [
    { name: 'webtool', summary: 'webtool: runs `npx -y webtool` from the project root' },
    { name: 'dbtool', summary: 'dbtool: connects to the project database' },
  ],
  ...overrides,
})

async function mountTab(record: ProjectMcpRecord, trusted = true) {
  vi.mocked(forebrainApi.projectMcp).mockResolvedValue(record)
  const wrapper = mount(ProjectMcp, {
    props: { project: projectRecord(trusted), projectId: 'p1' },
  })
  await flushPromises()
  return wrapper
}

describe('ProjectMcp', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectMcp').mockResolvedValue(recordOf())
    vi.spyOn(forebrainApi, 'projectMcpConsent').mockResolvedValue({ ok: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders the pending consent entries with their summaries', async () => {
    const wrapper = await mountTab(recordOf())
    expect(wrapper.find('[data-testid="project-mcp-pending"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('webtool')
    expect(wrapper.text()).toContain('webtool: runs `npx -y webtool` from the project root')
    expect(wrapper.text()).toContain('dbtool: connects to the project database')
  })

  it('records an allow through projectMcpConsent and reloads', async () => {
    const wrapper = await mountTab(recordOf())
    await wrapper.find('[data-testid="mcp-allow-webtool"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.projectMcpConsent).toHaveBeenCalledWith('p1', ['webtool'])
    // The reload after the decision ran.
    expect(forebrainApi.projectMcp).toHaveBeenCalledTimes(2)
  })

  it('records a decline with an empty allow list', async () => {
    const wrapper = await mountTab(recordOf())
    await wrapper.find('[data-testid="mcp-decline-dbtool"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.projectMcpConsent).toHaveBeenCalledWith('p1', [])
  })

  it('hides the confirmation block once no entries are pending', async () => {
    const wrapper = await mountTab(recordOf({ pendingConsent: [] }))
    expect(wrapper.find('[data-testid="project-mcp-pending"]').exists()).toBe(false)
    // The servers that would still load keep rendering.
    expect(wrapper.text()).toContain('globaltool')
  })
})
